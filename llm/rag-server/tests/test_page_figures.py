"""Getting the bytes: what a scraped page offers, and what is safe to download.

``describe_figures`` is already covered by test_figure_describer; everything here is
the half that is new for Confluence and ServiceNow — deciding which ``<img>`` is worth
a vision call, fetching it through the client's own authenticated session, and
refusing anything that would cost the scrape more than the picture is worth.
"""

import base64

import pytest

from rag.core.documents import page_figures

PNG = b"\x89PNG\r\n\x1a\n"
JPEG = b"\xff\xd8\xff\xe0"
GIF = b"GIF89a"


def _image(magic: bytes = PNG, size: int = 4096) -> bytes:
    """Enough bytes to clear MIN_IMAGE_BYTES, with a real magic number in front."""
    return magic + b"\x00" * max(0, size - len(magic))


def _webp(size: int = 4096) -> bytes:
    body = b"\x00" * max(0, size - 12)
    return b"RIFF" + b"\x00\x00\x00\x00" + b"WEBP" + body


class FakeResponse:
    def __init__(self, status_code=200, body=b"", content_type="image/png", content_length=None):
        self.status_code = status_code
        self.headers = {}
        if content_type is not None:
            self.headers["Content-Type"] = content_type
        if content_length is not None:
            self.headers["Content-Length"] = str(content_length)
        self.raw = _Raw(body)
        self._body = body
        self.closed = False

    def iter_content(self, chunk_size=8192):
        # As requests does it: already-decoded bytes, in chunks. The production code
        # reads this way because raw.read(n) counts pre-decode bytes and truncates a
        # compressed image.
        for i in range(0, len(self._body), chunk_size):
            yield self._body[i : i + chunk_size]

    # requests.Response is a context manager and closes on exit. stream=True keeps
    # the connection out of the pool until then, so every path must go through this.
    def __enter__(self):
        return self

    def __exit__(self, *exc):
        self.closed = True
        return False


class _Raw:
    def __init__(self, body):
        self._body = body

    def read(self, amount, decode_content=False):
        return self._body[:amount]


class FakeSession:
    """Records every URL it is asked for, so 'did not make the call' is testable."""

    def __init__(self, responses=None, raises=None):
        self.responses = responses or {}
        self.raises = raises or {}
        self.calls = []

    def get(self, url, **kwargs):
        self.calls.append(url)
        if url in self.raises:
            raise self.raises[url]
        return self.responses.get(url, FakeResponse(status_code=404, body=b""))


class FakeConfluence:
    def __init__(self, session, url, attachments=None, listing_error=None):
        self._session = session
        self.url = url
        self._attachments = attachments or []
        self._listing_error = listing_error

    def get_attachments_from_content(self, page_id):
        self.listings = getattr(self, "listings", 0) + 1
        if self._listing_error is not None:
            raise self._listing_error
        return {"results": self._attachments}


# ---------------------------------------------------------------- page_image_sources


def test_no_html_offers_no_pictures():
    assert page_figures.page_image_sources("") == []
    assert page_figures.page_image_sources("<p>no pictures here</p>") == []


def test_alt_text_is_kept_because_it_is_the_only_caption_a_page_gives():
    found = page_figures.page_image_sources('<img src="/download/a.png" alt="escalation matrix">')

    assert found == [{"src": "/download/a.png", "alt": "escalation matrix"}]


def test_chrome_is_not_content():
    # Every one of these is on a normal Confluence page, and describing them is
    # money spent on a smiley.
    html = """
      <img src="/images/icons/profilepics/default.png">
      <img src="/images/emoticons/smile.png">
      <img src="/s/1234/_/images/logo.png">
      <img src="https://cdn.example.com/avatar/u42.png">
      <img src="/download/attachments/1/diagram.png" alt="diagram">
    """
    found = page_figures.page_image_sources(html)

    assert [f["src"] for f in found] == ["/download/attachments/1/diagram.png"]


def test_the_same_picture_twice_on_a_page_is_one_picture():
    html = '<img src="/download/a.png"><img src="/download/a.png"><img src="/download/b.png">'

    assert [f["src"] for f in page_figures.page_image_sources(html)] == ["/download/a.png", "/download/b.png"]


def test_a_gallery_is_capped_not_described_in_full():
    html = "".join(f'<img src="/download/{i}.png">' for i in range(page_figures.MAX_FIGURES_PER_PAGE + 5))

    assert len(page_figures.page_image_sources(html)) == page_figures.MAX_FIGURES_PER_PAGE


def test_the_cap_is_caller_settable_for_a_cheaper_pass():
    html = "".join(f'<img src="/download/{i}.png">' for i in range(6))

    assert len(page_figures.page_image_sources(html, limit=2)) == 2


def test_an_inline_picture_is_not_filtered_on_the_text_of_its_payload():
    # _SKIP_PATH_MARKERS describes URL paths, and "/" is in the base64 alphabet, so a
    # long enough payload contains "/s/" by chance — measured at over half of ~80KB
    # images. Those were dropped before anything ever tried to decode them.
    # b"\x03\xfb\x3f" encodes to exactly "A/s/", so this is the real collision rather
    # than a contrived string — the same one random image bytes produce by chance.
    payload = base64.b64encode(b"\x03\xfb\x3f" * 2000).decode("ascii")
    src = f"data:image/png;base64,{payload}"
    assert "/s/" in src

    found = page_figures.page_image_sources(f'<img src="{src}" alt="pasted">')

    assert [f["alt"] for f in found] == ["pasted"]


def test_a_compressed_body_is_read_whole_not_truncated():
    # raw.read(n) counts bytes before decoding, so a gzipped image came back short —
    # and a short image is a corrupt one handed to the vision model.
    body = _image(size=200 * 1024)
    url = "https://wiki.example.com/big.png"
    session = FakeSession({url: FakeResponse(body=body)})
    fetch = page_figures.default_fetcher(session, "https://wiki.example.com")

    assert fetch(url) == body


# --------------------------------------------------------------------- _sniff_mime


@pytest.mark.parametrize(
    "data,expected",
    [
        (PNG + b"rest", "image/png"),
        (JPEG + b"rest", "image/jpeg"),
        (GIF + b"rest", "image/gif"),
        (_webp(64), "image/webp"),
        (b"<html>not an image</html>", None),
        (b"", None),
    ],
)
def test_the_bytes_decide_the_type_not_the_file_name(data, expected):
    assert page_figures._sniff_mime(data) == expected


# ------------------------------------------------------------------ default_fetcher


def test_an_inline_picture_needs_no_request():
    session = FakeSession()
    payload = base64.b64encode(_image()).decode("ascii")
    fetch = page_figures.default_fetcher(session, "https://wiki.example.com")

    assert fetch(f"data:image/png;base64,{payload}") == _image()
    assert session.calls == []


def test_a_malformed_data_uri_is_not_mistaken_for_a_url():
    session = FakeSession()
    fetch = page_figures.default_fetcher(session, "https://wiki.example.com")

    assert fetch("data:image/png,notbase64") is None
    assert session.calls == []


def test_a_huge_inline_image_is_refused_before_it_is_decoded():
    # A download is refused at the cap without being read into memory; an inline
    # image the page author pasted must not be the way round that.
    session = FakeSession()
    oversize = base64.b64encode(b"\x00" * (page_figures.MAX_IMAGE_BYTES + 1024)).decode("ascii")
    fetch = page_figures.default_fetcher(session, "https://wiki.example.com")

    assert fetch(f"data:image/png;base64,{oversize}") is None
    assert session.calls == []


def test_a_relative_src_resolves_against_the_instance_url():
    body = _image()
    session = FakeSession({"https://wiki.example.com/wiki/download/a.png": FakeResponse(body=body)})
    fetch = page_figures.default_fetcher(session, "https://wiki.example.com/wiki/")

    assert fetch("download/a.png") == body


def test_an_absolute_src_is_left_alone():
    body = _image()
    session = FakeSession({"https://other.example.com/a.png": FakeResponse(body=body)})
    fetch = page_figures.default_fetcher(session, "https://wiki.example.com")

    assert fetch("https://other.example.com/a.png") == body


def test_a_dead_link_is_asked_about_once_per_run():
    # One broken URL is embedded on many pages. Retrying it per page cost ~1.5s
    # each to learn the same thing.
    session = FakeSession()
    failed: set = set()
    fetch = page_figures.default_fetcher(session, "https://wiki.example.com", failed=failed)

    assert fetch("https://wiki.example.com/gone.png") is None
    assert fetch("https://wiki.example.com/gone.png") is None
    assert session.calls == ["https://wiki.example.com/gone.png"]


def test_a_connection_failure_is_remembered_too():
    url = "https://wiki.example.com/boom.png"
    session = FakeSession(raises={url: ConnectionResetError(54, "Connection reset by peer")})
    failed: set = set()
    fetch = page_figures.default_fetcher(session, "https://wiki.example.com", failed=failed)

    assert fetch(url) is None
    assert fetch(url) is None
    assert session.calls == [url]


def test_without_a_memo_every_page_pays_again():
    # The memo is opt-in per run, so a caller that does not pass one keeps the
    # previous behaviour rather than sharing state across integrations.
    session = FakeSession()
    fetch = page_figures.default_fetcher(session, "https://wiki.example.com")

    fetch("https://wiki.example.com/gone.png")
    fetch("https://wiki.example.com/gone.png")

    assert len(session.calls) == 2


def test_a_login_page_wearing_an_image_url_is_refused():
    url = "https://wiki.example.com/a.png"
    session = FakeSession({url: FakeResponse(body=b"<html>sign in</html>", content_type="text/html")})
    fetch = page_figures.default_fetcher(session, "https://wiki.example.com")

    assert fetch(url) is None


def test_an_exotic_image_type_the_provider_cannot_read_is_refused():
    url = "https://wiki.example.com/a.tif"
    session = FakeSession({url: FakeResponse(body=_image(), content_type="image/tiff")})
    fetch = page_figures.default_fetcher(session, "https://wiki.example.com")

    assert fetch(url) is None


def test_a_declared_oversize_is_refused_before_it_is_read():
    url = "https://wiki.example.com/huge.png"
    session = FakeSession(
        {url: FakeResponse(body=_image(), content_length=page_figures.MAX_IMAGE_BYTES + 1)},
    )
    fetch = page_figures.default_fetcher(session, "https://wiki.example.com")

    assert fetch(url) is None


@pytest.mark.parametrize("header", ["not-a-number", "", "12,345", "1.5e9"])
def test_a_junk_content_length_is_ignored_not_fatal(header):
    # Letting int() raise would skip a perfectly good image, log it as a fetch
    # failure, and add the URL to the run's failed memo — so every later page
    # carrying it is skipped too, for a server that was never unreachable.
    url = "https://wiki.example.com/a.png"
    body = _image()
    response = FakeResponse(body=body)
    response.headers["Content-Length"] = header
    session = FakeSession({url: response})
    failed: set = set()
    fetch = page_figures.default_fetcher(session, "https://wiki.example.com", failed=failed)

    assert fetch(url) == body
    assert failed == set()


def test_a_lying_content_length_does_not_buy_an_unbounded_read():
    url = "https://wiki.example.com/huge.png"
    oversize = _image(size=page_figures.MAX_IMAGE_BYTES + 10)
    session = FakeSession({url: FakeResponse(body=oversize, content_type="image/png")})
    fetch = page_figures.default_fetcher(session, "https://wiki.example.com")

    assert fetch(url) is None


def test_a_response_with_no_content_type_is_still_fetched():
    # Some Data Center instances serve attachments without one; the magic-number
    # check downstream is what actually decides.
    url = "https://wiki.example.com/a.png"
    body = _image()
    session = FakeSession({url: FakeResponse(body=body, content_type=None)})
    fetch = page_figures.default_fetcher(session, "https://wiki.example.com")

    assert fetch(url) == body


@pytest.mark.parametrize(
    "response,src",
    [
        (FakeResponse(status_code=404), "https://wiki.example.com/gone.png"),
        (FakeResponse(body=b"<html>", content_type="text/html"), "https://wiki.example.com/a.png"),
        (FakeResponse(body=_image(), content_type="image/tiff"), "https://wiki.example.com/a.tif"),
        (
            FakeResponse(body=_image(), content_length=page_figures.MAX_IMAGE_BYTES + 1),
            "https://wiki.example.com/h.png",
        ),
        (FakeResponse(body=_image(size=page_figures.MAX_IMAGE_BYTES + 10)), "https://wiki.example.com/h2.png"),
        (FakeResponse(body=_image()), "https://wiki.example.com/ok.png"),
    ],
)
def test_every_refusal_still_returns_the_connection(response, src):
    # stream=True checks a connection out of the pool and keeps it there until the
    # body is read or the response closed. Most paths below refuse without reading,
    # and a scrape walks thousands of images — leaking one per refusal exhausts the
    # pool and stalls the sync.
    session = FakeSession({src: response})
    fetch = page_figures.default_fetcher(session, "https://wiki.example.com")

    fetch(src)

    assert response.closed


def test_a_fetched_attachment_returns_its_connection_too():
    body = _image()
    download_url = "https://site.atlassian.net/wiki/download/a.png"
    for response in (FakeResponse(body=body), FakeResponse(status_code=403)):
        session = FakeSession({download_url: response})
        confluence = FakeConfluence(
            session,
            url="https://site.atlassian.net/wiki",
            attachments=[{"title": "a.png", "_links": {"download": "/download/a.png"}}],
        )
        page_figures.confluence_attachment_fetcher(confluence, "1", "https://site.atlassian.net")("/download/a.png")
        assert response.closed


# ----------------------------------------------------- confluence_attachment_fetcher


def test_an_attachment_is_fetched_through_its_rest_download_link():
    # Confluence Cloud serves the /download/attachments/... URL its own HTML
    # carries only to a browser session; an API token gets 401 there.
    body = _image()
    download_url = "https://site.atlassian.net/wiki/download/attachments/1/matrix.png?version=1"
    session = FakeSession({download_url: FakeResponse(body=body)})
    confluence = FakeConfluence(
        session,
        url="https://site.atlassian.net/wiki",
        attachments=[{"title": "matrix.png", "_links": {"download": "/download/attachments/1/matrix.png?version=1"}}],
    )
    fetch = page_figures.confluence_attachment_fetcher(confluence, "1", "https://site.atlassian.net")

    assert fetch("/download/attachments/1/matrix.png") == body


def test_the_download_root_comes_from_the_client_not_the_configured_host():
    # Cloud is served under /wiki and Data Center under its own context path.
    # Joining against the raw host loses the prefix and 404s every attachment.
    session = FakeSession()
    confluence = FakeConfluence(
        session,
        url="https://site.atlassian.net/wiki",
        attachments=[{"title": "a.png", "_links": {"download": "/download/attachments/1/a.png"}}],
    )
    fetch = page_figures.confluence_attachment_fetcher(confluence, "1", "https://site.atlassian.net")

    fetch("/download/attachments/1/a.png")

    assert session.calls == ["https://site.atlassian.net/wiki/download/attachments/1/a.png"]


def test_a_percent_encoded_file_name_still_matches_its_attachment():
    body = _image()
    download_url = "https://site.atlassian.net/wiki/download/attachments/1/escalation%20matrix.png"
    session = FakeSession({download_url: FakeResponse(body=body)})
    confluence = FakeConfluence(
        session,
        url="https://site.atlassian.net/wiki",
        attachments=[
            {
                "title": "escalation matrix.png",
                "_links": {"download": "/download/attachments/1/escalation%20matrix.png"},
            }
        ],
    )
    fetch = page_figures.confluence_attachment_fetcher(confluence, "1", "https://site.atlassian.net")

    assert fetch("/download/attachments/1/escalation%20matrix.png") == body


def test_a_page_with_no_images_costs_no_attachment_lookup():
    # A fetcher is built for every page walked, most of which carry no image. Listing
    # eagerly spent one Confluence API call per page to learn there was nothing there.
    session = FakeSession()
    confluence = FakeConfluence(session, url="https://site.atlassian.net/wiki", attachments=[])
    confluence.listings = 0

    page_figures.confluence_attachment_fetcher(confluence, "1", "https://site.atlassian.net")

    assert confluence.listings == 0


def test_the_attachment_list_is_fetched_once_however_many_images():
    body = _image()
    session = FakeSession({"https://site.atlassian.net/wiki/download/a.png": FakeResponse(body=body)})
    confluence = FakeConfluence(
        session,
        url="https://site.atlassian.net/wiki",
        attachments=[{"title": "a.png", "_links": {"download": "/download/a.png"}}],
    )
    fetch = page_figures.confluence_attachment_fetcher(confluence, "1", "https://site.atlassian.net")

    fetch("/download/a.png")
    fetch("/download/a.png")
    fetch("/download/missing.png")

    assert confluence.listings == 1


def test_no_session_is_reported_rather_than_raised():
    fetch = page_figures.default_fetcher(None, "https://wiki.example.com")

    assert fetch("https://wiki.example.com/a.png") is None


def test_something_pasted_in_from_elsewhere_is_not_requested_at_all():
    # Pages carry absolute <img> URLs copied from other sites. Those are someone
    # else's asset: often dead, never ours to authenticate against, and a request
    # each to find that out — 60 of them per sync, ~1.5s apiece.
    session = FakeSession()
    confluence = FakeConfluence(session, url="https://site.atlassian.net/wiki", attachments=[])
    fetch = page_figures.confluence_attachment_fetcher(confluence, "1", "https://site.atlassian.net")

    assert fetch("https://hasura.io/docs/2.0/assets/images/gql-backend.svg") is None
    assert session.calls == []


def test_an_inline_picture_on_a_confluence_page_needs_no_attachment_record():
    session = FakeSession()
    confluence = FakeConfluence(session, url="https://site.atlassian.net/wiki", attachments=[])
    fetch = page_figures.confluence_attachment_fetcher(confluence, "1", "https://site.atlassian.net")
    payload = base64.b64encode(_image()).decode("ascii")

    assert fetch(f"data:image/png;base64,{payload}") == _image()
    assert session.calls == []


def test_an_unreadable_attachment_listing_costs_the_pictures_not_the_sync():
    session = FakeSession()
    confluence = FakeConfluence(
        session,
        url="https://site.atlassian.net/wiki",
        listing_error=RuntimeError("403 from Confluence"),
    )
    fetch = page_figures.confluence_attachment_fetcher(confluence, "1", "https://site.atlassian.net")

    assert fetch("/download/attachments/1/a.png") is None


def test_an_attachment_that_will_not_download_is_reported_not_raised():
    session = FakeSession({}, raises={"https://site.atlassian.net/wiki/download/a.png": TimeoutError("slow")})
    confluence = FakeConfluence(
        session,
        url="https://site.atlassian.net/wiki",
        attachments=[{"title": "a.png", "_links": {"download": "/download/a.png"}}],
    )
    fetch = page_figures.confluence_attachment_fetcher(confluence, "1", "https://site.atlassian.net")

    assert fetch("/download/a.png") is None


# ------------------------------------------------------------- collect_page_figures


def test_a_page_of_pictures_becomes_figures_ready_to_describe():
    body = _image()
    html = '<img src="/download/a.png" alt="matrix">'

    figures = page_figures.collect_page_figures(html, lambda src: body)

    assert len(figures) == 1
    assert figures[0]["index"] == 1
    assert figures[0]["mime_type"] == "image/png"
    assert figures[0]["alt"] == "matrix"
    assert figures[0]["src"] == "/download/a.png"
    assert base64.b64decode(figures[0]["data"]) == body


def test_the_same_image_under_two_names_is_described_once():
    # A logo repeated across a page would otherwise cost one vision call each.
    html = '<img src="/download/a.png"><img src="/download/b.png">'

    figures = page_figures.collect_page_figures(html, lambda src: _image())

    assert len(figures) == 1


def test_different_images_both_survive_and_are_numbered_in_order():
    bodies = {"/download/a.png": _image(PNG), "/download/b.png": _image(JPEG)}
    html = '<img src="/download/a.png"><img src="/download/b.png">'

    figures = page_figures.collect_page_figures(html, lambda src: bodies[src])

    assert [f["index"] for f in figures] == [1, 2]
    assert [f["mime_type"] for f in figures] == ["image/png", "image/jpeg"]


def test_a_spacer_gif_is_below_the_floor():
    html = '<img src="/download/spacer.gif">'

    assert page_figures.collect_page_figures(html, lambda src: _image(GIF, size=64)) == []


def test_bytes_that_are_not_an_image_are_dropped_whatever_the_page_called_them():
    html = '<img src="/download/a.png">'

    assert page_figures.collect_page_figures(html, lambda src: b"<html>" + b"x" * 4096) == []


def test_a_picture_that_cannot_be_fetched_does_not_stop_the_rest():
    bodies = {"/download/a.png": None, "/download/b.png": _image()}
    html = '<img src="/download/a.png"><img src="/download/b.png">'

    figures = page_figures.collect_page_figures(html, lambda src: bodies[src])

    assert [f["src"] for f in figures] == ["/download/b.png"]


def test_indexes_close_the_gap_left_by_a_dropped_picture():
    bodies = {"/download/a.png": None, "/download/b.png": _image()}
    html = '<img src="/download/a.png"><img src="/download/b.png">'

    figures = page_figures.collect_page_figures(html, lambda src: bodies[src])

    assert figures[0]["index"] == 1


# ------------------------------------------------------------------ figure_chunk_text


def test_a_chunk_that_only_says_a_picture_exists_is_not_indexed():
    # It would retrieve for anything and answer nothing.
    figure = {"index": 1, "alt": "diagram", "summary": "", "transcription": ""}

    assert page_figures.figure_chunk_text("Runbook", "https://wiki/x", figure) is None


def test_a_transcription_alone_is_worth_indexing():
    figure = {"index": 1, "alt": "", "summary": "", "transcription": "restarts >= 400 -> CODE PURPLE"}

    text = page_figures.figure_chunk_text("Escalation", "https://wiki/x", figure)

    assert "Text in figure:" in text
    assert "CODE PURPLE" in text


def test_the_chunk_is_anchored_to_the_page_that_carries_it():
    figure = {"index": 2, "alt": "matrix", "summary": "An escalation table.", "transcription": "row one"}

    text = page_figures.figure_chunk_text("Escalation runbook", "https://wiki/x", figure)

    assert text.startswith("Escalation runbook — figure 2")
    assert "Caption: matrix" in text
    assert "Figure: An escalation table." in text
    assert "Text in figure:\nrow one" in text


def test_a_page_with_no_title_falls_back_to_its_url():
    figure = {"index": 1, "alt": "", "summary": "A diagram.", "transcription": ""}

    text = page_figures.figure_chunk_text("", "https://wiki/x", figure)

    assert text.startswith("https://wiki/x — figure 1")
