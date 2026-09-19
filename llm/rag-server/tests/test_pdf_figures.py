"""Tests for PDF page/figure extraction used by knowledge-base ingestion."""

import base64
import zlib

import pymupdf
import pytest

from rag.core.documents.pdf_figures import (
    HARD_MAX_IMAGES,
    extract_pdf_figures,
)


def _png_bytes(width: int, height: int, colour: bytes = b"\xff\x00\x00") -> bytes:
    """Build a minimal solid-colour PNG without pulling in Pillow."""

    def chunk(tag: bytes, payload: bytes) -> bytes:
        return len(payload).to_bytes(4, "big") + tag + payload + zlib.crc32(tag + payload).to_bytes(4, "big")

    header = width.to_bytes(4, "big") + height.to_bytes(4, "big") + bytes([8, 2, 0, 0, 0])
    raw = b"".join(b"\x00" + colour * width for _ in range(height))
    return b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", header) + chunk(b"IDAT", zlib.compress(raw)) + chunk(b"IEND", b"")


def _prose(page, body: str) -> None:
    """Lay down wrapped body text. insert_text does not wrap — a long string on
    one line is clipped to the page width, which leaves the page below the
    page-render text floor and makes fixtures lie about what they contain."""
    page.insert_textbox(pymupdf.Rect(72, 72, 520, 720), body, fontsize=9)


def _build_pdf(*, text_pages=1, image_size=None, vector_page=False, repeat_image_pages=0) -> bytes:
    doc = pymupdf.open()
    for i in range(text_pages):
        page = doc.new_page()
        _prose(page, f"Page {i + 1}. " + ("Runbook prose. " * 40))
    if image_size:
        page = doc.new_page()
        _prose(page, "Figure follows. " * 40)
        page.insert_image(pymupdf.Rect(72, 150, 472, 550), stream=_png_bytes(*image_size))
    for _ in range(repeat_image_pages):
        page = doc.new_page()
        _prose(page, "Repeat masthead. " * 40)
        page.insert_image(pymupdf.Rect(72, 150, 472, 550), stream=_png_bytes(*image_size))
    if vector_page:
        page = doc.new_page()
        # No text: a vector-only diagram page, which get_images() cannot see.
        # Each draw_rect is its own path, so this reads as a real diagram to
        # get_drawings() rather than as a single page-furniture rule.
        for offset in range(0, 300, 20):
            page.draw_rect(
                pymupdf.Rect(72 + offset, 100 + offset, 172 + offset, 200 + offset),
                color=(0, 0, 1),
            )
    out: bytes = doc.tobytes()
    doc.close()
    return out


def test_extracts_page_text():
    result = extract_pdf_figures(_build_pdf(text_pages=2))

    assert result["page_count"] == 2
    assert result["pages_processed"] == 2
    assert result["truncated"] is False
    assert "Runbook prose." in result["pages"][0]["text"]
    assert result["images_extracted"] == 0


def test_extracts_embedded_image_as_png():
    result = extract_pdf_figures(_build_pdf(text_pages=1, image_size=(200, 200)))

    figures = [img for page in result["pages"] for img in page["images"]]
    assert len(figures) == 1
    figure = figures[0]
    assert figure["kind"] == "embedded"
    assert figure["mime_type"] == "image/png"
    assert figure["index"] == 1
    assert base64.b64decode(figure["data"]).startswith(b"\x89PNG")
    assert len(figure["content_hash"]) == 64


def test_skips_decorative_images():
    # A 12x12 icon is below the pixel floor and must not cost a vision call.
    result = extract_pdf_figures(_build_pdf(text_pages=1, image_size=(12, 12)))

    assert result["images_extracted"] == 0
    assert all(page["images"] == [] for page in result["pages"])


def test_renders_vector_only_page():
    # The whole point: a diagram drawn as vectors is invisible to get_images(),
    # so the page itself has to be rasterised or the figure is lost.
    result = extract_pdf_figures(_build_pdf(text_pages=1, vector_page=True))

    kinds = [img["kind"] for page in result["pages"] for img in page["images"]]
    assert kinds == ["page_render"]


def test_deduplicates_repeated_image():
    # A masthead logo repeated on every page is one figure, not four.
    result = extract_pdf_figures(_build_pdf(text_pages=1, image_size=(200, 200), repeat_image_pages=3))

    assert result["images_extracted"] == 1
    # Deduplication is not a cap: nothing was lost, so images_skipped stays 0.
    assert result["images_deduped"] == 3
    assert result["images_skipped"] == 0


def test_image_cap_marks_truncated():
    result = extract_pdf_figures(_build_pdf(text_pages=1, image_size=(200, 200), vector_page=True), max_images=0)

    assert result["images_extracted"] == 0
    assert result["truncated"] is True
    assert result["images_skipped"] >= 1


def test_page_cap_marks_truncated():
    result = extract_pdf_figures(_build_pdf(text_pages=5), max_pages=2)

    assert result["page_count"] == 5
    assert result["pages_processed"] == 2
    assert result["truncated"] is True
    assert len(result["pages"]) == 2


def test_large_image_is_downscaled():
    result = extract_pdf_figures(_build_pdf(text_pages=1, image_size=(4000, 400)), max_image_edge=1024)

    figures = [img for page in result["pages"] for img in page["images"]]
    assert len(figures) == 1
    assert max(figures[0]["width"], figures[0]["height"]) <= 1024


def test_image_cap_is_bounded_by_hard_max():
    # A caller cannot raise the cap past the hard ceiling.
    result = extract_pdf_figures(_build_pdf(text_pages=1), max_images=HARD_MAX_IMAGES + 500)

    assert result["images_extracted"] == 0


def test_rejects_unreadable_pdf_as_a_user_error():
    # ValueError specifically: the endpoint maps it to a 400 with the reason.
    # Anything else becomes a 500, which tells the uploader nothing actionable.
    with pytest.raises(ValueError, match="corrupt or incomplete"):
        extract_pdf_figures(b"not a pdf at all")


def test_rejects_truncated_pdf_as_a_user_error():
    # Passes a %PDF- header check but has no body — a half-finished upload.
    with pytest.raises(ValueError, match="corrupt or incomplete"):
        extract_pdf_figures(b"%PDF-1.7\n%truncated")


def test_repeated_placement_of_one_image_is_extracted_once():
    """The same image placed several times on a page repeats its xref.

    Without a guard each placement is rendered and PNG-encoded before the
    content-hash pass discards the copies, which is the expensive half done
    for nothing.
    """
    import io

    import pymupdf

    from rag.core.documents import pdf_figures

    doc = pymupdf.open()
    page = doc.new_page()
    pix = pymupdf.Pixmap(pymupdf.csRGB, pymupdf.IRect(0, 0, 300, 300), False)
    pix.set_rect(pymupdf.IRect(0, 0, 300, 300), (200, 30, 30))
    png = pix.tobytes("png")
    for rect in [(50, 50, 150, 150), (200, 50, 300, 150), (50, 200, 150, 300), (200, 200, 300, 300)]:
        page.insert_image(pymupdf.Rect(*rect), stream=png)
    buf = io.BytesIO()
    doc.save(buf)
    doc.close()

    reopened = pymupdf.open(stream=buf.getvalue(), filetype="pdf")
    target = reopened[0]
    assert len(target.get_images(full=True)) == 4, "fixture should repeat one xref four times"

    found = pdf_figures._embedded_images(doc=reopened, page=target, min_pixels=1000, max_edge=1024)
    assert len(found) == 1
    reopened.close()


def test_render_dpi_is_capped_before_rasterising():
    """A large page must not be rendered full size and shrunk afterwards.

    An A0 page at 120 dpi is ~64MB before the shrink, and pages render
    concurrently on the embedding worker pool.
    """
    import pymupdf

    from rag.core.documents.pdf_figures import (
        DEFAULT_MAX_IMAGE_EDGE,
        DEFAULT_PAGE_RENDER_DPI,
        _render_dpi,
        _render_page,
    )

    doc = pymupdf.open()
    a0 = doc.new_page(width=2384, height=3370)
    capped = _render_dpi(a0, DEFAULT_PAGE_RENDER_DPI, DEFAULT_MAX_IMAGE_EDGE)
    assert capped < DEFAULT_PAGE_RENDER_DPI
    raw = a0.get_pixmap(dpi=capped)
    assert max(raw.width, raw.height) <= DEFAULT_MAX_IMAGE_EDGE

    a0.draw_rect(pymupdf.Rect(20, 20, 2364, 3350), color=(0, 0, 1), width=3)
    rendered = _render_page(a0, DEFAULT_PAGE_RENDER_DPI, DEFAULT_MAX_IMAGE_EDGE)
    assert rendered is not None
    assert max(rendered["width"], rendered["height"]) <= DEFAULT_MAX_IMAGE_EDGE

    # A page already within the cap keeps the configured dpi.
    small = doc.new_page(width=200, height=200)
    assert _render_dpi(small, DEFAULT_PAGE_RENDER_DPI, DEFAULT_MAX_IMAGE_EDGE) == DEFAULT_PAGE_RENDER_DPI
    doc.close()


def test_a_page_that_fails_to_encode_does_not_fail_the_document(monkeypatch):
    """One unreadable page must not take the whole extraction down.

    The embedded path already guarded render and encode together; the page-render
    path left encoding outside the try.
    """
    import pymupdf

    from rag.core.documents import pdf_figures

    doc = pymupdf.open()
    page = doc.new_page(width=300, height=300)

    def boom(_pix):
        raise MemoryError("png conversion failed")

    monkeypatch.setattr(pdf_figures, "_encode", boom)
    assert pdf_figures._render_page(page, 120, 1024) is None
    doc.close()
