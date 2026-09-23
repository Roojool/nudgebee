"""Unit tests for the verdict judge: the 3 verdicts, the retry-then-fail
path, and the should_skip_verdict evidence gate.

Same house style as test_ragas_evaluation.py — no real LLM calls, the
module's LLM-invoking function is monkeypatched so these run offline.
"""

import json
import unittest

from llm.agents.common import verdict_judge as mod
from llm.agents.common.verdict_judge import (
    JudgeVerdict,
    evaluate_verdict,
    should_skip_verdict,
)


def _canned(status: str) -> str:
    return json.dumps(
        {
            "status": status,
            "walkthrough": "walked the scenario",
            "agent_actions": "did things",
            "expected_path": "should have done other things",
            "comparison": "diverged here",
            "final_outcome": "ended up here",
            "reason": f"because {status}",
        }
    )


class EvaluateVerdictTest(unittest.TestCase):
    def _run_with_canned_response(self, text: str) -> JudgeVerdict:
        original = mod._invoke_llm
        mod._invoke_llm = lambda llm, prompt: text
        try:
            return evaluate_verdict({"test_id": "t1", "query": "q"}, llm=None)
        finally:
            mod._invoke_llm = original

    def test_passed_verdict(self):
        result = self._run_with_canned_response(_canned("passed"))
        self.assertFalse(result.failed)
        self.assertEqual(result.status, "passed")
        self.assertEqual(result.reason, "because passed")

    def test_partially_correct_verdict(self):
        result = self._run_with_canned_response(_canned("partially_correct"))
        self.assertFalse(result.failed)
        self.assertEqual(result.status, "partially_correct")

    def test_failed_verdict(self):
        result = self._run_with_canned_response(_canned("failed"))
        self.assertFalse(result.failed)
        self.assertEqual(result.status, "failed")

    def test_evaluator_failure_distinct_from_failed_verdict(self):
        """A judge crash must never be reported as status='failed' — that
        would be indistinguishable from a real "the agent failed" verdict."""
        original = mod._invoke_llm
        mod._invoke_llm = lambda llm, prompt: (_ for _ in ()).throw(
            RuntimeError("provider timeout")
        )
        try:
            result = evaluate_verdict({"test_id": "t1", "query": "q"}, llm=None)
        finally:
            mod._invoke_llm = original

        self.assertTrue(result.failed)
        self.assertIsNone(result.status)
        self.assertIn("provider timeout", result.error)

    def test_malformed_json_retries_once_then_succeeds(self):
        calls = {"n": 0}

        def flaky_invoke(llm, prompt):
            calls["n"] += 1
            if calls["n"] == 1:
                return "not json at all"
            return _canned("passed")

        original = mod._invoke_llm
        mod._invoke_llm = flaky_invoke
        try:
            result = evaluate_verdict({"test_id": "t1", "query": "q"}, llm=None)
        finally:
            mod._invoke_llm = original

        self.assertEqual(calls["n"], 2)
        self.assertFalse(result.failed)
        self.assertEqual(result.status, "passed")

    def test_unknown_status_value_is_treated_as_evaluator_failure(self):
        result = self._run_with_canned_response(_canned("maybe"))
        self.assertTrue(result.failed)
        self.assertIsNone(result.status)


class RealWrapperTest(unittest.TestCase):
    def test_works_through_ragas_langchain_wrapper(self):
        """Callers pass ragas' LangchainLLMWrapper, which has no .invoke."""
        from langchain_core.language_models.fake import FakeListLLM
        from ragas.llms import LangchainLLMWrapper

        llm = LangchainLLMWrapper(FakeListLLM(responses=[_canned("passed")]))
        result = evaluate_verdict({"test_id": "t1", "query": "q"}, llm)
        self.assertFalse(result.failed, result.error)
        self.assertEqual(result.status, "passed")


class PromptTest(unittest.TestCase):
    def test_long_trace_keeps_head_and_tail(self):
        trace = "START" + "x" * 50000 + "FINAL_OUTCOME"
        prompt = mod._build_prompt({"execution_trace": trace})
        self.assertIn("START", prompt)
        self.assertIn("FINAL_OUTCOME", prompt)
        self.assertIn("chars omitted", prompt)

    def test_setup_script_reaches_the_judge(self):
        prompt = mod._build_prompt({"setup": "kubectl apply -f broken-probe.yaml"})
        self.assertIn("kubectl apply -f broken-probe.yaml", prompt)


class BackgroundQueueTest(unittest.TestCase):
    def test_submit_stores_verdict_and_wait_returns(self):
        from langchain_core.language_models.fake import FakeListLLM
        from ragas.llms import LangchainLLMWrapper

        from benchmark_server.utils import run_manager

        stored = []
        original = run_manager.set_verdict
        run_manager.set_verdict = lambda *args: stored.append(args)
        try:
            llm = LangchainLLMWrapper(FakeListLLM(responses=[_canned("failed")]))
            context = {"test_id": "t1", "status": "fail", "execution_trace": "ran"}
            mod.submit_verdict("run1", 3, "conv-1", context, llm)
            self.assertTrue(mod.wait_for_verdicts("run1", timeout=10))
        finally:
            run_manager.set_verdict = original

        run_id, test_index, conversation_id, fields = stored[0]
        self.assertEqual((run_id, test_index, conversation_id), ("run1", 3, "conv-1"))
        self.assertEqual(fields["verdict_status"], "failed")
        self.assertIsNone(fields["verdict_error"])

    def test_no_evidence_clears_pending_without_calling_the_judge(self):
        from benchmark_server.utils import run_manager

        stored = []
        original = run_manager.set_verdict
        run_manager.set_verdict = lambda *args: stored.append(args)
        try:
            mod.submit_verdict("run2", 1, "conv-2", {"status": "fail"}, llm=object())
            self.assertTrue(mod.wait_for_verdicts("run2", timeout=10))
        finally:
            run_manager.set_verdict = original

        fields = stored[0][3]
        self.assertIsNone(fields["verdict_status"])
        self.assertIsNone(fields["verdict_error"])

    def test_evaluator_error_fields(self):
        fields = mod.verdict_fields(JudgeVerdict(failed=True, error="boom"))
        self.assertIsNone(fields["verdict_status"])
        self.assertEqual(fields["verdict_error"], "boom")


class ShouldSkipVerdictTest(unittest.TestCase):
    def test_skips_failed_test_with_no_trace(self):
        self.assertTrue(should_skip_verdict("fail", ""))
        self.assertTrue(should_skip_verdict("error", ""))

    def test_judges_failed_test_with_a_trace(self):
        """A crash mid-task has real evidence — still worth judging."""
        self.assertFalse(
            should_skip_verdict("fail", "tool_call: kubectl get pods\n...")
        )

    def test_never_skips_a_pass(self):
        self.assertFalse(should_skip_verdict("pass", ""))


if __name__ == "__main__":
    unittest.main()
