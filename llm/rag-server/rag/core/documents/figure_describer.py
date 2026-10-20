"""Turn a figure's pixels into the text that is indexed in its place.

Mirrors llm-server's ``agents/core/vision_adapter.go`` — the prompt and the
response format are kept identical so a figure described by either service is
indexed the same way. Ingestion moved here because this is where the sources and
the indexer already live; the per-account model resolution it needs
(``get_llm``) was already here too.

The transcription is not decoration. A reader searching the knowledge base types
a pod name, an error code or a panel title — literal strings that exist only
inside the picture. Prose about the picture matches none of them, so a
caption-only surrogate indexes a figure that can never be retrieved.
"""

import hashlib
import logging
import re
import threading
from concurrent.futures import ThreadPoolExecutor
from typing import Any, Dict, List, Optional, Tuple

from rag.core.documents import figure_cache
from rag.core.llm.providers import supports_vision
from utils.config import Config

logger = logging.getLogger(__name__)

FIGURE_PROMPT = """You are indexing a figure from a technical document for a DevOps/SRE knowledge base.
This is the ONLY record of this figure the rest of the system will ever see, so it must stand on its own.

Respond in exactly this format, with no other text before or after:

<summary>
{2-4 sentences: what kind of figure this is (architecture diagram, dashboard screenshot, flow chart, log output,
table, error message, photo), what it depicts, and what an engineer would learn from it. If it shows a system,
name the components and how they connect. If it shows data, state the trend and any anomaly. Be factual; do not
speculate beyond what is visible; make no recommendations.}
</summary>
<text>
{Every piece of text visible in the figure, transcribed VERBATIM — titles, labels, node names, axis labels,
legends, captions, error messages, code, numbers, units, timestamps. Preserve exact spelling, casing and
punctuation of identifiers; do not paraphrase, summarise or correct them. Keep related labels on one line
(e.g. "checkout-svc -> payments-api"). If the figure contains no text at all, write NONE.}
</text>"""

# Case-insensitive: the prompt asks for lowercase tags, but a model that capitalises them
# has still answered, and dropping its description over the casing would be a silent loss.
_SUMMARY_RE = re.compile(r"(?is)<summary>(.*?)</summary>")
_TEXT_RE = re.compile(r"(?is)<text>(.*?)</text>")

# A completion this short that carried neither tag is a refusal or an error
# message, not a description. Indexing it would put "I can't help with that" in
# the knowledge base AND count the figure as described, so no shortfall is
# reported for a figure nothing ever read.
_MIN_UNTAGGED_SUMMARY_CHARS = 80

# Provider errors that mean "this model cannot accept an image". Anything else —
# a timeout, a 429, a transient 5xx — must NOT be read as a missing capability,
# or one bad minute permanently marks an account as having no vision model.
_NO_VISION_MARKERS = (
    "validationexception",
    "does not support image",
    "does not support vision",
    "image input",
    "invalid_request_error",
    "unsupported",
    "modality",
)


def parse_description(content: str) -> Dict[str, str]:
    """Split a completion into its summary and verbatim-transcription halves."""
    content = (content or "").strip()
    if not content:
        return {"summary": "", "transcription": ""}

    out = {"summary": "", "transcription": ""}
    match = _SUMMARY_RE.search(content)
    if match:
        out["summary"] = match.group(1).strip()
    match = _TEXT_RE.search(content)
    if match:
        transcription = match.group(1).strip()
        # The prompt asks for NONE when a figure carries no text; storing that
        # literal would make "NONE" a searchable token on every wordless chart.
        if transcription.upper() != "NONE":
            out["transcription"] = transcription

    if not out["summary"] and not out["transcription"]:
        # A model that ignored the format still produced something usable, and an
        # unparsed description beats indexing nothing — but only if it is long
        # enough to be a description at all.
        if len(content) >= _MIN_UNTAGGED_SUMMARY_CHARS:
            out["summary"] = content
    return out


class _SkippedAfterNoVision(Exception):
    """Stands in for a figure that was never sent, because an earlier one already
    proved the model cannot accept images. Worded to match _NO_VISION_MARKERS so it
    reaches the same verdict as the real provider error it stands for."""

    def __init__(self) -> None:
        super().__init__("model does not support image input")


def looks_like_missing_vision(error: BaseException) -> bool:
    """Whether this failure means the model cannot see, rather than that the call
    went wrong."""
    message = f"{type(error).__name__} {error}".lower()
    return any(marker in message for marker in _NO_VISION_MARKERS)


def content_hash(data: str) -> str:
    return hashlib.sha256((data or "").encode("utf-8")).hexdigest()


def _describe_one(llm: Any, image: Dict[str, str]) -> Tuple[Dict[str, str], Optional[BaseException], int]:
    try:
        result = llm.generate_with_images(FIGURE_PROMPT, [image])
    except Exception as exc:  # one bad figure must not cost the document
        logger.warning("figure_describer: description failed: %s", exc)
        return {"summary": "", "transcription": ""}, exc, 0
    tokens = (result.input_tokens or 0) + (result.output_tokens or 0)
    return parse_description(result.text), None, tokens


def _record_cost(account_id, collection_name, llm, total_tokens, call_count, failures) -> None:
    """Persist what the vision calls cost this load."""
    if call_count <= 0:
        return
    try:
        from rag.core.embeddings.tracker import persist_figure_description_usage
        from utils.shared import get_provider_name

        persist_figure_description_usage(
            account_id=account_id,
            collection_name=collection_name,
            provider=get_provider_name(type(llm).__name__),
            model=getattr(llm, "model", "") or "",
            total_tokens=total_tokens,
            figure_count=call_count,
            status="success" if failures == 0 else "partial",
            error_message=None if failures == 0 else f"{failures} of {call_count} figures could not be described",
        )
    except Exception as exc:  # accounting must never fail an ingest
        logger.warning("figure_describer: could not record vision cost: %s", exc)


def _describe_each(llm: Any, figures: List[Dict[str, Any]], unique: List[Tuple[str, List[int]]]) -> List[Any]:
    """Describe one image per distinct hash, in parallel, stopping early if blind.

    A model that cannot accept an image says so on the first call and will say it on
    every one after, so the rest are skipped rather than bought: learning it once
    costs one request instead of 22 on a Confluence space. A transient failure is
    deliberately not treated this way — a timeout on one figure says nothing about
    the next.
    """
    concurrency = max(1, min(Config.kb_vision_concurrency, len(unique)))
    no_vision = threading.Event()

    def run(entry):
        digest, positions = entry
        if no_vision.is_set():
            return digest, ({"summary": "", "transcription": ""}, _SkippedAfterNoVision(), 0)
        first = figures[positions[0]]
        result = _describe_one(llm, {"mime_type": first["mime_type"], "data": first["data"]})
        if result[1] is not None and looks_like_missing_vision(result[1]):
            no_vision.set()
        return digest, result

    # The first alone. A wave of `concurrency` threads all launch before any of them
    # answers, so a blind model still charged for the whole first wave before the
    # verdict was in — 4 refusals at the default, not one.
    results = [run(unique[0])]
    rest = unique[1:]
    if not rest:
        return results
    if no_vision.is_set():
        results.extend(run(entry) for entry in rest)
        return results
    with ThreadPoolExecutor(max_workers=concurrency) as pool:
        results.extend(pool.map(run, rest))
    return results


def describe_figures(
    account_id: str,
    figures: List[Dict[str, Any]],
    llm: Any = None,
    use_cache: bool = True,
    collection_name: str = "figure_description",
) -> Tuple[int, bool]:
    """Fill in each figure's ``summary`` and ``transcription`` in place.

    Identical images are described once however many positions they occupy, so a
    logo repeated on every page costs one call rather than forty — and an image
    described by an earlier load is not sent again at all.

    Returns ``(described_count, vision_available)``. ``vision_available`` is
    False only when the provider cannot take an image at all — never because a
    call timed out — so a bad minute is reported as "could not analyse" rather
    than as a missing model.
    """
    if not figures:
        return 0, True

    if llm is None:
        # Imported here: the generator module opens a database connection at
        # import time, and this module is importable without one.
        from rag.core.embeddings.generator import get_llm

        try:
            llm = get_llm(account_id)
        except Exception as exc:
            # No usable credential for this account. That is the same outcome as a
            # provider that cannot see — figures go undescribed — and must not travel
            # up as an exception, because the caller reads one as "this PDF is corrupt"
            # and tells the uploader their file is unreadable.
            # Broad on purpose: a missing credential raises ValueError, but a failed
            # decrypt or a database blip raises something else, and any of them
            # reaching the caller is read as "this PDF is corrupt".
            #
            # The traceback follows that split rather than the call. An account with no
            # credential configured is an expected state and its message says all there
            # is to say; anything else is a fault this broad catch would otherwise hide,
            # and is worth the stack.
            logger.warning(
                "figure_describer: no usable provider, %d figures left undescribed: %s",
                len(figures),
                exc,
                exc_info=not isinstance(exc, ValueError),
            )
            return 0, False
    if not supports_vision(llm):
        logger.info(
            "figure_describer: provider %s cannot accept images; %d figures left undescribed",
            type(llm).__name__,
            len(figures),
        )
        return 0, False

    by_hash: Dict[str, List[int]] = {}
    for index, figure in enumerate(figures):
        digest = figure.get("content_hash") or content_hash(figure.get("data", ""))
        figure["content_hash"] = digest
        by_hash.setdefault(digest, []).append(index)

    model = getattr(llm, "model", "") or ""
    cached = figure_cache.load(account_id, by_hash.keys(), model) if use_cache else {}
    for digest, positions in by_hash.items():
        hit = cached.get(digest)
        if hit and (hit.get("summary") or hit.get("transcription")):
            for position in positions:
                figures[position].update(hit)

    # Only images nothing has described yet reach the model.
    unique = [(digest, positions) for digest, positions in by_hash.items() if digest not in cached]
    if not unique:
        return len(figures), True

    results = _describe_each(llm, figures, unique)

    vision_available = True
    total_tokens = 0
    failures = 0
    for digest, (description, error, tokens) in results:
        total_tokens += tokens
        if error is not None:
            if looks_like_missing_vision(error):
                vision_available = False
            # A figure never sent is not a call that failed. Counting it made the
            # cost record say "8 of 1 figures could not be described".
            if not isinstance(error, _SkippedAfterNoVision):
                failures += 1
        for position in by_hash[digest]:
            figures[position].update(description)

    calls_made = sum(1 for _, (_, error, _) in results if not isinstance(error, _SkippedAfterNoVision))
    _record_cost(account_id, collection_name, llm, total_tokens, calls_made, failures)

    if use_cache:
        figure_cache.store(account_id, figures, model, collection_name)

    described = sum(1 for f in figures if f.get("summary") or f.get("transcription"))
    return described, vision_available
