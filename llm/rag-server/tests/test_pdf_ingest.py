"""End to end: an uploaded PDF becomes the documents it is indexed as.

Uses a real PDF built with pymupdf — a synthetic extraction dict would not catch
the parts that go wrong in practice, which are all about what pymupdf actually
reports for a page.
"""

import base64
import zlib

import pymupdf
import pytest

from rag.core.documents import pdf_ingest


def _png_bytes(width: int, height: int, colour: bytes = b"\xff\x00\x00") -> bytes:
    """A minimal solid-colour PNG, without pulling in Pillow."""

    def chunk(tag: bytes, payload: bytes) -> bytes:
        return len(payload).to_bytes(4, "big") + tag + payload + zlib.crc32(tag + payload).to_bytes(4, "big")

    header = width.to_bytes(4, "big") + height.to_bytes(4, "big") + bytes([8, 2, 0, 0, 0])
    raw = b"".join(b"\x00" + colour * width for _ in range(height))
    return b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", header) + chunk(b"IDAT", zlib.compress(raw)) + chunk(b"IEND", b"")


def _build_pdf(*, text_pages=1, image_size=None, repeat_image_pages=0) -> bytes:
    """Built here rather than imported from the extractor's tests: importing across test
    modules makes mypy see one file under two module names and fail the lint."""
    doc = pymupdf.open()
    for i in range(text_pages):
        page = doc.new_page()
        page.insert_textbox(pymupdf.Rect(72, 72, 520, 720), f"Page {i + 1}. " + ("Runbook prose. " * 40), fontsize=9)
    if image_size:
        page = doc.new_page()
        page.insert_textbox(pymupdf.Rect(72, 72, 520, 720), "Figure follows. " * 40, fontsize=9)
        page.insert_image(pymupdf.Rect(72, 150, 472, 550), stream=_png_bytes(*image_size))
    for _ in range(repeat_image_pages):
        page = doc.new_page()
        page.insert_textbox(pymupdf.Rect(72, 72, 520, 720), "Repeat masthead. " * 40, fontsize=9)
        page.insert_image(pymupdf.Rect(72, 150, 472, 550), stream=_png_bytes(*image_size))
    out: bytes = doc.tobytes()
    doc.close()
    return out


def _described(figures):
    for figure in figures:
        figure["summary"] = "A latency dashboard."
        figure["transcription"] = "RB-K8S-MEM-007"
    return len(figures), True


def _undescribed(figures):
    return 0, False


@pytest.fixture
def describe(monkeypatch):
    def use(fn):
        monkeypatch.setattr(pdf_ingest, "describe_figures", lambda account_id, figures, collection_name="": fn(figures))

    return use


def test_pdf_becomes_page_and_figure_documents(describe):
    describe(_described)
    data = base64.b64encode(_build_pdf(text_pages=2, image_size=(200, 200))).decode()

    documents, warning = pdf_ingest.build_pdf_documents(data, "acct", "runbook.pdf")

    texts = [d.page_content for d in documents]
    assert any(t.startswith("runbook.pdf — page 1 of 3") for t in texts)
    figure_chunks = [t for t in texts if "figure 1" in t]
    assert len(figure_chunks) == 1
    # The literal string from inside the image is what a query matches.
    assert "RB-K8S-MEM-007" in figure_chunks[0]
    assert warning == ""
    assert documents[0].metadata["filename"] == "runbook.pdf"


def test_undescribed_figures_are_not_indexed_but_are_reported(describe):
    describe(_undescribed)
    data = base64.b64encode(_build_pdf(text_pages=1, image_size=(200, 200))).decode()

    documents, warning = pdf_ingest.build_pdf_documents(data, "acct", "runbook.pdf")

    # A chunk that only says "there is a figure here" retrieves noise.
    assert not any("figure 1" in d.page_content for d in documents)
    assert "no vision-capable model" in warning


def test_deduplicated_figures_are_not_reported_as_a_cap(describe):
    # The bug this pins: a masthead repeated on every page used to be counted
    # against the figure limit, telling the user to raise a cap nothing hit.
    describe(_described)
    data = base64.b64encode(_build_pdf(text_pages=1, image_size=(200, 200), repeat_image_pages=3)).decode()

    _, warning = pdf_ingest.build_pdf_documents(data, "acct", "runbook.pdf")

    assert "figure limit" not in warning
    assert warning == ""


def test_rejects_a_file_that_is_not_a_pdf():
    with pytest.raises(ValueError, match="not a valid PDF"):
        pdf_ingest.build_pdf_documents(base64.b64encode(b"just text").decode(), "acct", "x.pdf")


def test_rejects_broken_base64():
    with pytest.raises(ValueError, match="could not be read"):
        pdf_ingest.build_pdf_documents("not base64 at all!!", "acct", "x.pdf")


def test_decode_pdf_accepts_a_header_preceded_by_junk():
    """Readers tolerate leading bytes before %PDF-, and so does pymupdf.

    Requiring the header at offset 0 would reject a document the extractor
    downstream opens without complaint.
    """
    import base64 as _b64

    from rag.core.documents.pdf_ingest import decode_pdf

    body = b"%PDF-1.7\n" + b"x" * 100
    shifted = b"\r\n\x00junk-prefix\r\n" + body
    assert decode_pdf(_b64.b64encode(shifted).decode()) == shifted


def test_decode_pdf_still_rejects_a_file_with_no_header():
    import base64 as _b64

    import pytest

    from rag.core.documents.pdf_ingest import decode_pdf

    with pytest.raises(ValueError, match="not a valid PDF"):
        decode_pdf(_b64.b64encode(b"just some text, no magic number").decode())


def test_decode_pdf_rejects_a_header_beyond_the_search_window():
    import base64 as _b64

    import pytest

    from rag.core.documents.pdf_ingest import decode_pdf

    buried = b"z" * 2000 + b"%PDF-1.7\n"
    with pytest.raises(ValueError, match="not a valid PDF"):
        decode_pdf(_b64.b64encode(buried).decode())


def test_figure_caps_come_from_config(monkeypatch):
    """The caps were operator-tunable before extraction moved to rag-server.

    Losing that silently would contradict what the knowledge-base docs promise,
    and an account with large documents could not be raised without a release.
    """
    from rag.core.documents import pdf_ingest
    from utils.config import Config

    seen = {}

    def fake_extract(pdf_bytes, **kwargs):
        seen.update(kwargs)
        return {"pages": [], "images_skipped": 0, "images_deduped": 0, "truncated": False, "page_count": 0}

    monkeypatch.setattr(pdf_ingest.pdf_figures, "extract_pdf_figures", fake_extract)
    monkeypatch.setattr(Config, "kb_pdf_max_pages", 7, raising=False)
    monkeypatch.setattr(Config, "kb_pdf_max_images", 3, raising=False)

    import base64 as _b64

    import pytest

    # The empty extraction trips the "no readable content" guard, which is fine:
    # the caps are read on the way in, which is what this pins.
    with pytest.raises(ValueError):
        pdf_ingest.build_pdf_documents(
            _b64.b64encode(b"%PDF-1.7\n" + b"x" * 50).decode(),
            account_id="acct",
        )
    assert seen["max_pages"] == 7
    assert seen["max_images"] == 3
