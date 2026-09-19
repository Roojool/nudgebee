"""Extract page text and figures from a PDF for knowledge-base ingestion.

A PDF's figures are invisible to ``page.get_text()``: an architecture diagram,
a dashboard screenshot or a scanned page carries no text layer at all, so a
text-only ingest indexes the caption and drops the thing the caption refers to.
This module pulls both halves out so the caller can send each figure to a
vision model and index the text it produces.

Two kinds of figure are emitted, because one mechanism does not catch both:

``embedded``
    A raster drawn into the page (``page.get_images``). Screenshots and
    exported charts arrive this way.

``page_render``
    The whole page rasterised. This is the only way to see a diagram drawn as
    *vector* operations — which is what Visio, draw.io and Mermaid exports
    become once they are in a PDF — and the only way to see a scanned page.
    Triggered when a page has visual content but almost no extractable text.

Known limitation: a vector diagram on a text-rich page is missed, because the
page-render trigger keys off a text floor. Catching it would mean rendering
nearly every page of every document, which costs a vision call per page.
"""

import base64
import hashlib
import logging
from typing import Any, Dict, List, Optional

import pymupdf

logger = logging.getLogger(__name__)

# Only png and jpeg are accepted by the vision allowlist on the llm-server side
# (agents/core/image_utils.go), so everything is normalised to PNG here rather
# than shipping a format the describer will reject.
_OUTPUT_MIME = "image/png"

# Defaults chosen so one document cannot monopolise the KB embedding worker
# pool. The caller may lower them; it may not raise them past the hard caps.
DEFAULT_MAX_PAGES = 200
DEFAULT_MAX_IMAGES = 25
DEFAULT_MIN_IMAGE_PIXELS = 10_000  # ~100x100; below this it is an icon or a rule
DEFAULT_MAX_IMAGE_EDGE = 1024  # vision models gain nothing above this
DEFAULT_PAGE_RENDER_TEXT_FLOOR = 200  # chars; below this a page is "not text"
DEFAULT_PAGE_RENDER_DPI = 120

HARD_MAX_PAGES = 1000
HARD_MAX_IMAGES = 100

# A shape this elongated is a divider, a spacer or a colour bar, never a figure.
_MAX_ASPECT_RATIO = 20

# Fewer drawing operations than this is page furniture — a footer rule, a table
# border, an underline — not a diagram worth a vision call.
_MIN_PAGE_RENDER_DRAWINGS = 5


def _normalize_pixmap(pix: pymupdf.Pixmap, max_edge: int) -> pymupdf.Pixmap:
    """Return an RGB, no-alpha pixmap whose longest edge is <= max_edge."""
    # CMYK and alpha-carrying pixmaps cannot be encoded as PNG directly, and a
    # 1-channel greyscale one can — but converting it costs nothing and keeps
    # the output format uniform.
    if pix.alpha or pix.colorspace is None or pix.colorspace.n not in (1, 3):
        pix = pymupdf.Pixmap(pymupdf.csRGB, pix)

    shrink_by = 0
    longest = max(pix.width, pix.height)
    # shrink(n) divides both edges by 2^n. Cap the exponent so a pathological
    # image cannot loop, and so we never shrink an image out of existence.
    while longest > max_edge and shrink_by < 4:
        shrink_by += 1
        longest //= 2
    if shrink_by:
        pix.shrink(shrink_by)
    return pix


def _is_decorative(width: int, height: int, min_pixels: int) -> bool:
    """Reject logos, icons, spacers and rules before they cost a vision call."""
    if width <= 0 or height <= 0:
        return True
    if width * height < min_pixels:
        return True
    longest, shortest = max(width, height), min(width, height)
    return longest / shortest > _MAX_ASPECT_RATIO


def _encode(pix: pymupdf.Pixmap) -> Dict[str, Any]:
    data = pix.tobytes("png")
    return {
        "mime_type": _OUTPUT_MIME,
        "data": base64.b64encode(data).decode("ascii"),
        "width": pix.width,
        "height": pix.height,
        "size_bytes": len(data),
        "content_hash": hashlib.sha256(data).hexdigest(),
    }


def _embedded_images(
    doc: pymupdf.Document,
    page: pymupdf.Page,
    min_pixels: int,
    max_edge: int,
) -> List[Dict[str, Any]]:
    """Pull the raster images drawn into one page."""
    out: List[Dict[str, Any]] = []
    # One image placed several times on a page is returned once per placement, and the
    # later content-hash pass would only discard the copies after each had been rendered
    # and PNG-encoded.
    seen_xrefs: set[int] = set()
    for item in page.get_images(full=True):
        xref = item[0]
        if xref in seen_xrefs:
            continue
        seen_xrefs.add(xref)
        try:
            info = doc.extract_image(xref)
        except Exception as exc:  # a damaged xref must not fail the document
            logger.warning("pdf_figures: could not extract image xref=%s: %s", xref, exc)
            continue
        if not info:
            continue
        if _is_decorative(info.get("width", 0), info.get("height", 0), min_pixels):
            continue
        try:
            encoded = _encode(_normalize_pixmap(pymupdf.Pixmap(doc, xref), max_edge))
        except Exception as exc:
            logger.warning("pdf_figures: could not render image xref=%s: %s", xref, exc)
            continue
        encoded["kind"] = "embedded"
        out.append(encoded)
    return out


def _should_render_page(page: pymupdf.Page, text: str, text_floor: int) -> bool:
    """Decide whether a page has to be rasterised to be seen at all.

    Only vector drawings qualify. A raster on the page — including a scanned
    page, which is one big raster — is already picked up by the embedded path,
    so rendering it again would buy a duplicate at the price of a vision call.
    And a page whose only raster was rejected as decorative is a near-blank
    page: rendering it describes an icon on whitespace.
    """
    if len(text.strip()) >= text_floor:
        return False
    try:
        drawings = page.get_drawings()
    except Exception:  # get_drawings is the flakiest call in this module
        return False
    return len(drawings) >= _MIN_PAGE_RENDER_DRAWINGS


def _render_dpi(page: pymupdf.Page, dpi: int, max_edge: int) -> int:
    """Cap the render DPI so the pixmap is born within ``max_edge``.

    Rendering first and shrinking afterwards allocates the full-size bitmap:
    an A0 page at 120 dpi is ~64MB before the shrink, on a worker pool that
    renders pages concurrently.
    """
    rect = page.rect
    longest_pt = max(rect.width, rect.height)
    if longest_pt <= 0:
        return dpi
    if longest_pt * dpi / 72.0 <= max_edge:
        return dpi
    return max(1, int(max_edge * 72.0 / longest_pt))


def _render_page(page: pymupdf.Page, dpi: int, max_edge: int) -> Optional[Dict[str, Any]]:
    try:
        # _normalize_pixmap still runs: it fixes colorspace and alpha, and trims any
        # edge left over from rounding the dpi down.
        pix = _normalize_pixmap(page.get_pixmap(dpi=_render_dpi(page, dpi, max_edge)), max_edge)
        # Encoding is inside the try for the same reason the render is: PNG conversion of
        # a large pixmap can fail on memory, and one unreadable page must not take the
        # whole document down. The embedded path already guards both together.
        encoded = _encode(pix)
    except Exception as exc:
        logger.warning("pdf_figures: could not render page %s: %s", page.number, exc)
        return None
    encoded["kind"] = "page_render"
    return encoded


def extract_pdf_figures(
    pdf_bytes: bytes,
    max_pages: int = DEFAULT_MAX_PAGES,
    max_images: int = DEFAULT_MAX_IMAGES,
    min_image_pixels: int = DEFAULT_MIN_IMAGE_PIXELS,
    max_image_edge: int = DEFAULT_MAX_IMAGE_EDGE,
    page_render_text_floor: int = DEFAULT_PAGE_RENDER_TEXT_FLOOR,
    page_render_dpi: int = DEFAULT_PAGE_RENDER_DPI,
) -> Dict[str, Any]:
    """Extract per-page text and bounded figures from a PDF.

    Identical images are emitted once (a logo on the masthead of every page is
    one figure, not forty) — the first position wins, and later occurrences are
    counted under ``images_deduped``. ``images_skipped`` counts only what a cap
    cost, so the two are never confused in what the user is told. Callers get ``truncated`` set when
    a cap stopped the extraction early, so a partial result is never mistaken
    for a complete one.
    """
    max_pages = max(1, min(max_pages, HARD_MAX_PAGES))
    max_images = max(0, min(max_images, HARD_MAX_IMAGES))

    try:
        doc = pymupdf.open(stream=pdf_bytes, filetype="pdf")
    except Exception as exc:
        # A truncated upload or a mislabelled file is the user's problem to fix,
        # not a server fault: raise the type the caller turns into a 400 with a
        # reason, rather than letting pymupdf's error surface as a 500.
        raise ValueError("this file could not be read as a PDF; it may be corrupt or incomplete") from exc

    try:
        if doc.is_encrypted and not doc.authenticate(""):
            raise ValueError("the PDF is password-protected")

        page_count = doc.page_count
        pages_processed = min(page_count, max_pages)

        pages: List[Dict[str, Any]] = []
        seen_hashes: set[str] = set()
        images_extracted = 0
        images_skipped = 0
        images_deduped = 0
        truncated = page_count > pages_processed

        for index in range(pages_processed):
            page = doc.load_page(index)
            text = page.get_text() or ""

            candidates = _embedded_images(page=page, doc=doc, min_pixels=min_image_pixels, max_edge=max_image_edge)
            if _should_render_page(page, text, page_render_text_floor):
                rendered = _render_page(page, page_render_dpi, max_image_edge)
                if rendered is not None:
                    candidates.append(rendered)

            kept: List[Dict[str, Any]] = []
            for candidate in candidates:
                if candidate["content_hash"] in seen_hashes:
                    # Counted apart from the cap: a repeated masthead is a
                    # figure we already have, not one the caps cost us, and
                    # reporting it as a cap would tell the user to raise a limit
                    # that was never reached.
                    images_deduped += 1
                    continue
                if images_extracted >= max_images:
                    images_skipped += 1
                    truncated = True
                    continue
                seen_hashes.add(candidate["content_hash"])
                candidate["index"] = len(kept) + 1
                kept.append(candidate)
                images_extracted += 1

            pages.append({"page": index + 1, "text": text, "images": kept})

        return {
            "page_count": page_count,
            "pages_processed": pages_processed,
            "pages": pages,
            "images_extracted": images_extracted,
            "images_skipped": images_skipped,
            "images_deduped": images_deduped,
            "truncated": truncated,
        }
    finally:
        doc.close()
