import json
import logging
import random
import threading
import uuid
from dataclasses import dataclass
from dataclasses import field

from sqlalchemy import create_engine
from sqlalchemy import text
from sqlalchemy.pool import QueuePool

from utils.config import Config, DBConfig

# Create a thread-safe connection pool
engine = create_engine(DBConfig.url, poolclass=QueuePool, pool_size=5, max_overflow=20, pool_pre_ping=True)

# Candidate text is kept only to make a trace gradeable after the fact — enough
# to judge against the query, not enough to store the corpus a second time. The
# document itself is already in Qdrant and identified by ``ref``.
SNIPPET_CHARS = 300


def _coerce_uuid(value):
    # conversation_id / message_id are UUID columns; non-UUID callers (tests,
    # warmup) store NULL rather than failing the insert.
    if not value:
        return None
    try:
        return str(uuid.UUID(str(value)))
    except (ValueError, AttributeError):
        return None


@dataclass
class TraceCandidate:
    """One candidate the search scored, whether or not it was returned.

    The dropped ones are the point: without them a deliberate "nothing here is
    relevant" is indistinguishable from a search that found nothing.
    """

    rank_in: int  # position after retrieval, before scoring
    ref: str | None  # parent document id
    chunk: str  # the matched chunk, truncated to SNIPPET_CHARS on write
    status: str  # returned | dropped_rank | dropped_floor
    rank_out: int | None = None  # position after scoring; None when dropped
    sim: float | None = None  # cosine similarity from retrieval
    rr: float | None = None  # cross-encoder score
    weighted: float | None = None  # the blended score the ranking used
    n_chunks: int = 1  # chunks this parent contributed to the candidate set
    reason: str | None = None
    # How a person recognises the document. ``ref`` is a parent id or a slice of
    # raw text, which names nothing to a reader, so the title and link the
    # document already carries are recorded alongside it.
    title: str | None = None
    url: str | None = None

    def to_json(self):
        out = {
            "rank_in": self.rank_in,
            "rank_out": self.rank_out,
            "ref": self.ref,
            "snippet": (self.chunk or "")[:SNIPPET_CHARS],
            "status": self.status,
            "n_chunks": self.n_chunks,
        }
        for key, value in (("sim", self.sim), ("rr", self.rr), ("weighted", self.weighted)):
            if value is not None:
                out[key] = round(float(value), 4)
        for key, value in (("title", self.title), ("url", self.url), ("reason", self.reason)):
            if value:
                out[key] = value
        return out


@dataclass
class RetrievalTrace:
    """One retrieval, as it happened.

    ``trace_id`` is supplied by the caller so the service that consumes the
    results can update this same row with what it did with them — which
    candidate it chose, what reached the prompt — instead of writing a second
    row that has to be joined back.
    """

    cloud_account_id: str
    module: str | None
    query: str
    trace_id: str | None = None
    conversation_id: str | None = None
    message_id: str | None = None
    tenant_id: str | None = None
    requested_k: int | None = None
    n_retrieved: int | None = None
    n_injected: int | None = None
    retrieval_ms: int | None = None
    score_ms: int | None = None
    fallback_reason: str | None = None
    scorer_version: str | None = None
    candidates: list[TraceCandidate] = field(default_factory=list)


def save_trace_async(trace: RetrievalTrace):
    """Persist a trace off the request path.

    Tracing must never be able to fail a search, so this is fire-and-forget and
    save_trace swallows everything.
    """
    if Config.trace_sample_rate < 1.0 and random.random() > Config.trace_sample_rate:
        return
    threading.Thread(target=save_trace, args=(trace,), daemon=True).start()


def save_trace(trace: RetrievalTrace):
    try:
        with engine.connect() as connection:
            with connection.begin():
                statement = text(
                    "INSERT INTO llm_retrieval_trace "
                    "(id, cloud_account_id, module, query, candidates, conversation_id, message_id, "
                    " tenant_id, requested_k, n_retrieved, n_injected, retrieval_ms, score_ms, "
                    " fallback_reason, scorer_version, created_at) "
                    "VALUES (COALESCE(:id, gen_random_uuid()), :cloud_account_id, :module, :query, "
                    " :candidates, :conversation_id, :message_id, :tenant_id, :requested_k, "
                    " :n_retrieved, :n_injected, :retrieval_ms, :score_ms, :fallback_reason, "
                    " :scorer_version, now())"
                )
                connection.execute(
                    statement,
                    {
                        "id": _coerce_uuid(trace.trace_id),
                        "cloud_account_id": trace.cloud_account_id,
                        "module": trace.module,
                        "query": trace.query,
                        "candidates": json.dumps([c.to_json() for c in trace.candidates]),
                        "conversation_id": _coerce_uuid(trace.conversation_id),
                        "message_id": _coerce_uuid(trace.message_id),
                        "tenant_id": _coerce_uuid(trace.tenant_id),
                        "requested_k": trace.requested_k,
                        "n_retrieved": trace.n_retrieved,
                        "n_injected": trace.n_injected,
                        "retrieval_ms": trace.retrieval_ms,
                        "score_ms": trace.score_ms,
                        "fallback_reason": trace.fallback_reason,
                        "scorer_version": trace.scorer_version,
                    },
                )
    except Exception as e:
        logging.exception("Error saving retrieval trace: %s", e)
