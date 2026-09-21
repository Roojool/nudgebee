"""Explore-mode scoring: RAGAS/verdict-judge answer quality (same machinery
diagnosis_scorer.py uses) plus a deterministic citation-grounding check that
catches a failure mode LLM-judge scoring alone can miss — a plausible-
sounding answer that doesn't actually point at the real buggy file.
"""

from __future__ import annotations

from typing import Any

from diagnosis_scorer import get_judges
from llm.agents.common.ragas_evaluation import evaluate_single
from llm.agents.common.verdict_judge import evaluate_verdict


def score_explore(scenario: dict[str, Any], response: dict[str, Any]) -> dict[str, Any]:
    llm, embeddings = get_judges()
    expected = scenario.get("expected_diagnosis") or []
    reference = "\n".join(expected)
    answer = response.get("answer") or ""
    query = scenario["explore"]["query"]

    eval_result = evaluate_single(query, answer, reference, llm, embeddings)
    verdict = evaluate_verdict(
        {
            "query": query,
            "expected_answer": reference,
            "actual_answer": answer,
            "test_id": scenario["name"],
        },
        llm,
    )

    return {
        "similarity": eval_result.similarity,
        "quality": eval_result.quality,
        "verdict": None if verdict.failed else verdict.status,
        "citation_grounded": citation_grounded(scenario, response),
    }


def citation_grounded(scenario: dict[str, Any], response: dict[str, Any]) -> bool:
    """True if at least one citation names the real buggy file. No check
    configured means vacuously true, mirroring code_fix_verifier's
    empty-test-list convention."""
    expected_substr = (scenario.get("expected_citation") or {}).get("file_contains")
    if not expected_substr:
        return True
    citations = response.get("citations") or []
    return any(expected_substr in (c.get("file_path") or "") for c in citations)
