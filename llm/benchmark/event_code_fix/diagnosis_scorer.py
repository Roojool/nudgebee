"""Diagnosis-stage scoring — a thin wrapper around the machinery nb_scenarios
already uses (llm/agents/common/ragas_evaluation.py, verdict_judge.py), so
this suite's fixture-drift/fix-verification code is the only genuinely new
scoring logic (code_fix_verifier.py).
"""

from __future__ import annotations

from typing import Any

from ragas.llms import LangchainLLMWrapper

from benchmark_server.common.llm import get_embeddings, get_llm
from llm.agents.common.metrics import get_token_metrics
from llm.agents.common.ragas_evaluation import evaluate_single
from llm.agents.common.verdict_judge import evaluate_verdict

# Matches events.SessionIdPrefixEventRCA in
# llm/llm-server/events/event_analyzer_repository.go:121, applied to the
# event_fingerprint the RCA response itself returns (event_analyzer.go:1445
# builds the session the same way). Not independently derivable from the
# alert payload — ingestion may normalize the fingerprint, so this always
# reads it back from the response, never from the alert the harness sent.
_RCA_SESSION_PREFIX = "event-rca-"

_llm = None
_embeddings = None


def get_judges():
    """Shared RAGAS/verdict-judge LLM handle — also used by explore_scorer.py,
    so both scorers judge against the same model instead of each spinning up
    their own."""
    global _llm, _embeddings
    if _llm is None:
        _llm = LangchainLLMWrapper(get_llm())
        _embeddings = get_embeddings()
    return _llm, _embeddings


def diagnosis_text(rca_response: dict[str, Any]) -> str:
    """The RCA response's diagnosis prose — what gets scored against
    expected_diagnosis, by this module and by the dashboard engine alike."""
    return "\n\n".join(
        filter(
            None,
            [
                rca_response.get("summary"),
                rca_response.get("investigation"),
                rca_response.get("analysis"),
            ],
        )
    )


def rca_session_id(rca_response: dict[str, Any]) -> str | None:
    """LLM session id of the diagnosis stage, for token/cost lookups."""
    fingerprint = rca_response.get("event_fingerprint")
    return _RCA_SESSION_PREFIX + fingerprint if fingerprint else None


def score_diagnosis(
    scenario: dict[str, Any], rca_response: dict[str, Any]
) -> dict[str, Any]:
    """Score one scenario's diagnosis text against its expected_diagnosis bullets.

    Returns similarity/quality (0-100, from RAGAS) and a verdict
    (passed/partially_correct/failed/None-on-judge-error, from the verdict
    judge) — the same two-pronged approach nb_scenarios uses, invoked as a
    library instead of inside common/benchmark.py's pytest fixture, since this
    suite's execution shape (multi-stage async + clone/apply/test) doesn't fit
    that engine (see the "Where it lives" section of the plan).
    """
    llm, embeddings = get_judges()
    expected = scenario.get("expected_diagnosis") or []
    reference = "\n".join(expected)
    answer = diagnosis_text(rca_response)
    query = scenario["alert"]["summary"]

    eval_result = evaluate_single(query, answer, reference, llm, embeddings)

    verdict = evaluate_verdict(
        {
            "query": query,
            "expected_answer": reference,
            "actual_answer": answer,
            "status": rca_response.get("status"),
            "test_id": scenario["name"],
        },
        llm,
    )

    return {
        "similarity": eval_result.similarity,
        "quality": eval_result.quality,
        "reason": eval_result.reason,
        "verdict": None if verdict.failed else verdict.status,
        "verdict_reason": verdict.reason,
    }


def diagnosis_stage_metrics(
    rca_response: dict[str, Any], account_id: str, tenant_id: str, user_id: str
) -> dict[str, Any] | None:
    """Tool-call count / tokens / cost for the diagnosis stage, via the same
    llm_conversation_token_usage lookup nb_scenarios uses. Requires
    APP_DATABASE_URL. Returns None if the session can't be resolved or has no
    usage rows yet — never raises, since this is a supplementary metric, not
    part of correctness scoring.

    Only the diagnosis stage: the fix stage's CodeAgent2 conversation runs
    under a separate session not exposed on EventAnalysisResponse, so its
    tool-call/token metrics aren't captured yet — a known gap, not a guess.
    """
    session_id = rca_session_id(rca_response)
    if not session_id:
        return None
    return get_token_metrics(session_id, account_id, tenant_id, user_id)
