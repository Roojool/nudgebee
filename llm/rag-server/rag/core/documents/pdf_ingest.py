"""Turn an uploaded PDF into the documents it is indexed as.

Extract → describe the figures → assemble chunks. This is the whole of a PDF
knowledge-base load; llm-server hands over the bytes and gets back a document
count and whatever the load could not do.

A shortfall is never silent: a page cap, a figure cap or an undescribed figure
is reported on the knowledge base, because a partially indexed document that
looks complete is worse than one that says what it is missing.
"""

import base64
import binascii
import logging
from typing import Any, Dict, List, Optional, Tuple

from rag.core.documents import pdf_figures
from rag.core.documents.figure_describer import describe_figures
from rag.core.documents.pdf_chunks import build_chunk_texts
from rag.core.types import Document
from utils.config import Config

logger = logging.getLogger(__name__)

_HEADER_SEARCH_BYTES = 1024


def decode_pdf(data: str) -> bytes:
    """Decode the stored base64 payload, rejecting what is not a PDF."""
    try:
        # Not validate=True: a payload that picked up a newline in transit is still a
        # readable PDF, and the magic-number check below is what actually decides whether
        # this is one.
        raw = base64.b64decode(data or "")
    except (binascii.Error, ValueError) as exc:
        raise ValueError("the uploaded file could not be read; please re-select it and try again") from exc
    # Searched within the first 1024 bytes rather than required at offset 0: readers
    # tolerate a header preceded by junk, and pymupdf opens such a file happily — so
    # demanding a prefix would reject a document our own extractor can read.
    if b"%PDF-" not in raw[:_HEADER_SEARCH_BYTES]:
        raise ValueError("this file is not a valid PDF")
    return raw


def load_warning(
    extraction: Dict[str, Any],
    figure_count: int,
    described: int,
    vision_available: bool,
) -> str:
    """What the load left out, or "" when it left out nothing."""
    notes: List[str] = []

    if extraction["pages_processed"] < extraction["page_count"]:
        notes.append(f"only the first {extraction['pages_processed']} of {extraction['page_count']} pages were indexed")
    # Only cap skips are reported. Deduplicated figures were not lost — the
    # description of the first occurrence covers them.
    if extraction.get("images_skipped"):
        notes.append(f"{extraction['images_skipped']} figures were skipped because the figure limit was reached")

    undescribed = figure_count - described
    if undescribed > 0:
        if not vision_available:
            notes.append(
                f"{undescribed} figures were stored but not analysed because no vision-capable model is configured"
            )
        else:
            notes.append(f"{undescribed} of {figure_count} figures could not be analysed")

    if not notes:
        return ""
    return "Indexed with gaps: " + "; ".join(notes) + "."


def build_pdf_documents(
    data: str,
    account_id: str,
    filename: str = "",
    context: Optional[Dict[str, Any]] = None,
    collection_name: str = "",
) -> Tuple[List[Document], str]:
    """The documents a PDF is indexed as, plus any shortfall worth reporting."""
    extraction = pdf_figures.extract_pdf_figures(
        decode_pdf(data),
        max_pages=Config.kb_pdf_max_pages,
        max_images=Config.kb_pdf_max_images,
    )

    figures: List[Dict[str, Any]] = []
    for page in extraction["pages"]:
        for image in page["images"]:
            figures.append({**image, "page": page["page"]})

    # The collection is threaded through so the vision cost lands against this
    # knowledge base, and so its cached descriptions can be dropped with it.
    described, vision_available = describe_figures(
        account_id, figures, collection_name=collection_name or "figure_description"
    )

    chunk_texts = build_chunk_texts(
        filename,
        [{"page": p["page"], "text": p["text"]} for p in extraction["pages"]],
        extraction["page_count"],
        figures,
    )
    if not chunk_texts:
        raise ValueError(
            "no readable content was found in this PDF (it may be empty or image-only with no analysable figures)"
        )

    base_metadata = dict(context or {})
    if filename:
        base_metadata["filename"] = filename
    documents = [Document(page_content=text, metadata=dict(base_metadata)) for text in chunk_texts]

    logger.info(
        "pdf_ingest: %s -> %d chunks (pages=%d indexed=%d figures=%d described=%d)",
        filename or "document",
        len(documents),
        extraction["page_count"],
        extraction["pages_processed"],
        len(figures),
        described,
    )
    return documents, load_warning(extraction, len(figures), described, vision_available)
