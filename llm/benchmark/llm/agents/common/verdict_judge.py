"""Verdict judge: an independent LLM evaluator that walks a benchmark
test's full execution evidence end-to-end and returns a qualitative
verdict — passed / partially_correct / failed — plus the reasoning
behind it.

Unlike ragas_evaluation.py's rubric scores (a single 1-5 number reduced to a
percentage), this judge returns a multi-field narrative and is invoked
directly against the configured eval LLM (``benchmark_server.common.llm.
get_llm``) with a plain prompt, rather than through ragas's PydanticPrompt.
That class routes LangChain LLMs through a path ragas itself marks
deprecated, and ``get_llm()`` can return either a plain completion LLM
(Bedrock/HuggingFace/VertexAI) or a chat model — only some of which support
structured-output tool calling. A plain prompt + JSON parse works uniformly
across both, the same way ``ragas_evaluation._call_rubric_prompt`` retries
once on a malformed judge response.
"""

import json
import logging
import threading
from concurrent.futures import Future, ThreadPoolExecutor, wait
from dataclasses import dataclass
from typing import Dict, Optional, Set

from pydantic import BaseModel
from ragas.prompt.utils import extract_json

logger = logging.getLogger(__name__)

VALID_STATUSES = ("passed", "partially_correct", "failed")

# Keep the prompt (and cost) bounded. The head shows how the agent framed the
# problem; the tail holds its last actions and the outcome, so keep both.
_TRACE_HEAD_CHARS = 8000
_TRACE_TAIL_CHARS = 12000
_MAX_SETUP_CHARS = 4000


def should_skip_verdict(status: str, execution_trace: str) -> bool:
    """True when there isn't enough evidence to walk the scenario.

    A failed/errored test with no execution trace at all (the agent never
    produced a single tool call — a setup failure, a benchmark-harness
    timeout before the conversation started, ...) has nothing for the judge
    to reconstruct from beyond a placeholder answer like "SYSTEM FAILURE" —
    it would either refuse or invent a narrative. A failed test WITH a trace
    (agent ran, took a wrong path, crashed mid-task) has real evidence and
    should still be judged — that's a legitimate "Failed".
    """
    return status in ("fail", "error") and not execution_trace


class VerdictOutput(BaseModel):
    status: str
    walkthrough: str
    agent_actions: str
    expected_path: str
    comparison: str
    final_outcome: str
    reason: str


@dataclass
class JudgeVerdict:
    """Result of the verdict judge.

    ``failed`` distinguishes an evaluator crash (judge LLM errored, or
    returned output that didn't parse) from a genuine ``status="failed"``
    verdict — callers must never collapse the two into the same UI state.
    """

    status: Optional[str] = None
    walkthrough: str = ""
    agent_actions: str = ""
    expected_path: str = ""
    comparison: str = ""
    final_outcome: str = ""
    reason: str = ""
    failed: bool = False
    error: str = ""


_INSTRUCTION = """You are a meticulous human benchmark reviewer grading one test from an
AI agent benchmark suite. Your job is to walk through the ENTIRE scenario the
way a careful reviewer would — not to skim the final answer and assign a label.

Reconstruct the scenario in this order:
Task -> Intended scenario -> Broken/unusual condition -> Agent execution ->
Agent decisions/actions -> Expected correct behavior -> Differences -> Final outcome -> Verdict

Do this in seven steps:

1. Scenario setup — what the user asked for, what the benchmark scenario
   represents, what was intentionally broken or unusual, why that condition
   matters, and what a correct solution is expected to accomplish.
2. What the agent encountered — the evidence the agent had (errors, signals,
   tool output), and which parts of the problem it correctly or incorrectly
   interpreted.
3. What the agent actually did — summarize the meaningful steps of its
   execution (not a raw trace dump): what it attempted, why it mattered,
   what happened, whether it was appropriate and whether it moved the task
   toward the intended outcome.
4. What the agent should have done — the correct path through THIS scenario,
   grounded in the actual setup and evidence given, not generic best
   practices.
5. Step-by-step comparison — where the agent followed the correct path,
   where it deviated, which deviations were harmless vs. consequential, and
   which requirements remained unmet.
6. Final outcome — whether the scenario ended in the intended state. Do NOT
   treat the agent saying "done" or "fixed" as evidence of success; judge
   from the observed execution and resulting state.
7. Verdict — exactly one of:
   - "passed": the agent correctly understood the scenario, handled the
     broken condition appropriately, completed the required work, and
     achieved the intended substantive outcome.
   - "partially_correct": the agent solved meaningful parts of the scenario
     or showed the right direction, but an important action, requirement,
     validation, or final outcome remained incomplete or incorrect.
   - "failed": the agent fundamentally failed to solve the scenario,
     misunderstood the task/broken condition, took an incorrect approach, or
     did not achieve the intended outcome.

Do not use numeric thresholds and do not derive the verdict from any
similarity/relevancy/quality score — those are separate metrics you do not
have access to. Make your own qualitative judgment from the evidence below.

The benchmark setup script, when present, is what created the scenario —
read it to find the intentionally broken condition rather than guessing it
from the expected answer alone.

Respond with ONLY a JSON object (no markdown fences, no commentary) with
exactly these keys, each a string. Respect the word limits — the whole
response must stay short enough to never be cut off:
{"status": "passed | partially_correct | failed",
 "walkthrough": "scenario setup and what the agent encountered (steps 1-2), max 120 words",
 "agent_actions": "what the agent actually did, in meaningful chronological steps (step 3), max 120 words",
 "expected_path": "what the agent should have done and why (step 4), max 100 words",
 "comparison": "where actual behavior matched or diverged from the expected path (step 5), max 100 words",
 "final_outcome": "what actually happened at the end of the scenario (step 6), max 50 words",
 "reason": "concise justification for the final verdict, max 40 words"}
"""


def _truncate(text: str, head: int, tail: int = 0) -> str:
    if not text:
        return "—"
    if len(text) <= head + tail:
        return text
    dropped = len(text) - head - tail
    return (
        text[:head]
        + f"\n[... {dropped} chars omitted ...]\n"
        + (text[-tail:] if tail else "")
    )


def _build_prompt(context: dict) -> str:
    tool_names = context.get("tool_names") or []
    trace = _truncate(
        context.get("execution_trace") or "", _TRACE_HEAD_CHARS, _TRACE_TAIL_CHARS
    )
    evidence = f"""
Task (user's query): {context.get('query') or '—'}

Benchmark setup script (creates the scenario, including any intentionally
broken condition): {_truncate(context.get('setup') or '', _MAX_SETUP_CHARS)}

Expected/reference answer: {context.get('expected_answer') or '—'}

Agent's final answer: {context.get('actual_answer') or '—'}

Execution status: {context.get('status') or '—'}
Error category: {context.get('error_category') or '—'}
Error message: {context.get('error_message') or '—'}

Tools used: {', '.join(tool_names) if tool_names else '—'}
Tool calls: {context.get('tool_calls_successful', 0)}/{context.get('tool_calls_total', 0)} successful

Tags: {', '.join(context.get('tags') or []) or '—'}

Agent execution trace (plan + tool calls):
{trace}
"""
    return _INSTRUCTION + "\n-----------------------------\n" + evidence + "\nOutput:"


def _invoke_llm(llm, prompt: str) -> str:
    """Call the configured eval LLM with a plain prompt and return its text.

    Callers hold ragas' ``LangchainLLMWrapper`` (no ``.invoke``), so unwrap to
    the LangChain model. That can be a chat model (``.invoke`` returns a
    message with ``.content``) or a completion LLM (returns a str).
    """
    llm = getattr(llm, "langchain_llm", llm)
    result = llm.invoke(prompt)
    return getattr(result, "content", result)


def _parse_output(text: str) -> VerdictOutput:
    data = json.loads(extract_json(text))
    output = VerdictOutput(**data)
    if output.status not in VALID_STATUSES:
        raise ValueError(f"judge returned unknown status: {output.status!r}")
    return output


def evaluate_verdict(context: dict, llm) -> JudgeVerdict:
    """Run the verdict judge once, with one retry on a malformed response.

    ``context`` carries whatever evidence is available: query,
    expected_answer, actual_answer, execution_trace, tool_names,
    tool_calls_total, tool_calls_successful, error_message, error_category,
    status, tags, test_id. Missing keys are fine — the prompt renders "—"
    for anything absent.

    A judge crash or unparseable response returns ``failed=True`` with
    ``status=None`` — never guesses "failed" as the verdict for an
    evaluator error, since that would be indistinguishable from a real
    "the agent failed the task" verdict.
    """
    prompt = _build_prompt(context)
    test_id = context.get("test_id", "?")

    last_error: Exception = RuntimeError("unreachable")
    for attempt in (1, 2):
        try:
            text = _invoke_llm(llm, prompt)
            output = _parse_output(text)
            return JudgeVerdict(
                status=output.status,
                walkthrough=output.walkthrough,
                agent_actions=output.agent_actions,
                expected_path=output.expected_path,
                comparison=output.comparison,
                final_outcome=output.final_outcome,
                reason=output.reason,
            )
        except Exception as e:  # noqa: BLE001 - judge I/O can fail in many ways
            last_error = e
            if attempt == 1:
                logger.warning(
                    "Verdict judge failed for %s, retrying once: %s", test_id, e
                )
                continue
            logger.error("Verdict judge failed for %s: %s", test_id, e)

    return JudgeVerdict(failed=True, error=str(last_error))


def verdict_fields(verdict: JudgeVerdict) -> dict:
    """The three verdict_* columns for a verdict (all set, so a pending row clears)."""
    if verdict.failed:
        return {
            "verdict_status": None,
            "verdict_result": None,
            "verdict_error": verdict.error,
        }
    return {
        "verdict_status": verdict.status,
        "verdict_result": {
            "status": verdict.status,
            "walkthrough": verdict.walkthrough,
            "agent_actions": verdict.agent_actions,
            "expected_path": verdict.expected_path,
            "comparison": verdict.comparison,
            "final_outcome": verdict.final_outcome,
            "reason": verdict.reason,
        },
        "verdict_error": None,
    }


# ---------------------------------------------------------------------------
# Background queue: the test worker saves its result with status "pending",
# hands the judge call here and moves on to the next test.
# ---------------------------------------------------------------------------

# Bounded so a large run doesn't fire every judge call at the provider at once.
# ponytail: in-process queue — a server restart drops in-flight calls; the
# sweeper clears rows pending >10 min back to "not judged" and /re-evaluate
# fills them in. Move to a durable queue if verdicts must survive restarts.
_POOL = ThreadPoolExecutor(max_workers=3, thread_name_prefix="verdict-judge")
_PENDING: Dict[str, Set[Future]] = {}
_PENDING_LOCK = threading.Lock()


def _judge_and_store(
    run_id: str,
    test_index: int,
    conversation_id: str,
    context: dict,
    llm,
    account_id: Optional[str],
) -> None:
    from benchmark_server.utils.run_manager import set_verdict

    try:
        if not context.get("execution_trace") and account_id and conversation_id:
            from llm.agents.common.metrics import (
                get_execution_trace,
                get_planner_response,
            )

            context["execution_trace"] = get_execution_trace(
                conversation_id, account_id
            ) or (get_planner_response(conversation_id, account_id) or "")

        if should_skip_verdict(
            context.get("status", ""), context.get("execution_trace", "")
        ):
            fields = {
                "verdict_status": None,
                "verdict_result": None,
                "verdict_error": None,
            }
        else:
            if llm is None:
                from ragas.llms import LangchainLLMWrapper

                from benchmark_server.common.llm import get_llm

                llm = LangchainLLMWrapper(get_llm())
            fields = verdict_fields(evaluate_verdict(context, llm))
    except Exception as e:  # noqa: BLE001 - never leave the row stuck on pending
        logger.exception(
            "Background verdict judge failed for %s", context.get("test_id")
        )
        fields = {
            "verdict_status": None,
            "verdict_result": None,
            "verdict_error": str(e),
        }
    set_verdict(run_id, test_index, conversation_id, fields)


def submit_verdict(
    run_id: str,
    test_index: int,
    conversation_id: str,
    context: dict,
    llm=None,
    account_id: Optional[str] = None,
) -> None:
    """Judge a test in the background; the verdict is written to its row when ready.

    The row should already be stored with ``verdict_status="pending"``.
    ``conversation_id`` guards the write: if the test was retried meanwhile,
    the stale verdict is dropped. Without an ``execution_trace`` in
    ``context`` and with ``account_id`` set, the trace is fetched in the
    background too. ``llm=None`` builds the configured eval LLM.
    """
    future = _POOL.submit(
        _judge_and_store, run_id, test_index, conversation_id, context, llm, account_id
    )
    with _PENDING_LOCK:
        _PENDING.setdefault(run_id, set()).add(future)

    def _done(f: Future) -> None:
        with _PENDING_LOCK:
            futures = _PENDING.get(run_id)
            if futures is not None:
                futures.discard(f)
                if not futures:
                    _PENDING.pop(run_id, None)

    future.add_done_callback(_done)


def wait_for_verdicts(run_id: str, timeout: float) -> bool:
    """Block until this run's background judge calls finish, or ``timeout`` seconds.

    Returns True when none are left running.
    """
    with _PENDING_LOCK:
        futures = set(_PENDING.get(run_id, ()))
    if not futures:
        return True
    logger.info(
        "Run %s: waiting up to %ds for %d verdict(s)",
        run_id,
        timeout,
        len(futures),
    )
    _, not_done = wait(futures, timeout=timeout)
    if not_done:
        logger.warning(
            "Run %s: %d verdict(s) still running after %ds; completing anyway",
            run_id,
            len(not_done),
            timeout,
        )
    return not not_done
