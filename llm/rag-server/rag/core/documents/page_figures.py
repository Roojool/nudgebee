"""Read the pictures on a scraped page, so an instruction inside one is indexed.

A wiki page puts part of its instruction in a picture: an arrow pointing at a button,
text typed onto a screenshot, a label saying what a column means. The scrape has
always dropped all of it — ``convert_img`` kept the alt text and threw the image
away — so the assistant read past the gap and answered as if the page were complete.

This is the same treatment PDF figures already get, pointed at a different source.
``describe_figures`` does the work and is source-agnostic: it takes images, fills in a
summary and a verbatim transcription, and dedupes by content hash so a logo repeated
on forty pages costs one vision call. What is new here is getting the bytes.

Confluence renders an image as ``<img src="/download/attachments/...">`` — relative to
the instance, behind the same authentication as the page — so the fetch has to reuse
the client's session rather than issue a bare GET. ServiceNow articles are the same
shape with a different prefix.

Every stage is capped, because this runs on the scrape's timer with a worker pool
shared by every integration. A page that trips a cap is described as far as the cap
allows and reported, never silently truncated.
"""

import base64
import hashlib
import logging
from typing import Any, Callable, Dict, List, Optional
from urllib.parse import unquote, urljoin, urlparse

from bs4 import BeautifulSoup

logger = logging.getLogger(__name__)

# A runbook page with more pictures than this is a gallery, not a procedure, and the
# vision spend stops being proportionate to what a reader would actually follow.
MAX_FIGURES_PER_PAGE = 8

# Refused before download: a provider rejects an oversized image anyway, and reading
# it into memory first is how a scrape worker dies on someone's 40MB screenshot.
MAX_IMAGE_BYTES = 8 * 1024 * 1024

# Emoticons and spacers are the bulk of what a wiki page's markup calls an image.
MIN_IMAGE_BYTES = 2 * 1024

_READ_CHUNK_BYTES = 64 * 1024

_IMAGE_TYPES = ("image/png", "image/jpeg", "image/jpg", "image/gif", "image/webp")

# Confluence serves avatars, emoticons and UI chrome from these paths. They are not
# content, and describing them is money spent on a smiley.
_SKIP_PATH_MARKERS = ("/images/icons/", "/images/emoticons/", "/s/", "/_/", "avatar")


def _is_probably_content(src: str) -> bool:
    lowered = src.lower()
    return not any(marker in lowered for marker in _SKIP_PATH_MARKERS)


def _sniff_mime(data: bytes) -> Optional[str]:
    """The image type as the bytes declare it.

    Not the URL suffix and not the server's header: a wiki serves attachments under
    names that lie, and the provider rejects a payload whose declared type does not
    match what it decodes.
    """
    if data.startswith(b"\x89PNG\r\n\x1a\n"):
        return "image/png"
    if data.startswith(b"\xff\xd8\xff"):
        return "image/jpeg"
    if data.startswith(b"GIF87a") or data.startswith(b"GIF89a"):
        return "image/gif"
    if data[:4] == b"RIFF" and data[8:12] == b"WEBP":
        return "image/webp"
    return None


def _declared_length(value: Optional[str]) -> Optional[int]:
    """Content-Length as a number, or None when the server did not give a usable one."""
    try:
        return int(value) if value else None
    except (TypeError, ValueError):
        return None


def _read_capped(response: Any) -> Optional[bytes]:
    """The body, or None if it runs past the cap.

    Through ``iter_content`` rather than ``raw.read(n)``: raw reads n bytes *before*
    decoding, so a gzipped image comes back short, and a short image is a corrupt one
    the vision model is then asked to read.
    """
    data = bytearray()
    for chunk in response.iter_content(chunk_size=_READ_CHUNK_BYTES):
        if not chunk:
            continue
        data.extend(chunk)
        if len(data) > MAX_IMAGE_BYTES:
            return None
    return bytes(data)


def page_image_sources(html: str, limit: int = MAX_FIGURES_PER_PAGE) -> List[Dict[str, str]]:
    """The images worth describing on a rendered page, in document order.

    Data URIs are returned inline; everything else is returned as a src to fetch.
    """
    if not html:
        return []
    found: List[Dict[str, str]] = []
    seen = set()
    for img in BeautifulSoup(html, "html.parser").find_all("img"):
        src = (img.get("src") or "").strip()
        if not src or src in seen:
            continue
        # The markers below describe URL paths. A data URI has no path — it is the
        # picture — and its base64 alphabet includes "/", so a long enough payload
        # contains "/s/" by chance roughly half the time. Filtering one on its text
        # silently dropped real inline images.
        if not src.startswith("data:") and not _is_probably_content(src):
            continue
        seen.add(src)
        found.append({"src": src, "alt": (img.get("alt") or "").strip()})
        if len(found) >= limit:
            break
    return found


def _decode_data_uri(src: str) -> Optional[bytes]:
    if not src.startswith("data:"):
        return None
    header, _, payload = src.partition(",")
    if "base64" not in header or not payload:
        return None
    # Checked on the encoded text, before decoding: a download is refused at the cap
    # without ever being read into memory, and an inline image the page author pasted
    # should cost no more. base64 carries 3 bytes per 4 characters.
    if len(payload) > (MAX_IMAGE_BYTES // 3 + 1) * 4:
        logger.info("page_figures: an inline image is over the byte cap; not described")
        return None
    try:
        return base64.b64decode(payload, validate=False)
    except Exception:
        return None


def default_fetcher(session: Any, base_url: str, failed: Optional[set] = None) -> Callable[[str], Optional[bytes]]:
    """Download one image with the session that is already authenticated for the page.

    Relative srcs are resolved against the instance's own base URL — the one
    Confluence reports in its responses — because reconstructing it from configured
    host gets Data Center's context path wrong.
    """

    def fetch(src: str) -> Optional[bytes]:
        inline = _decode_data_uri(src)
        if inline is not None:
            return inline
        if session is None:
            # Said once, plainly. Without this the AttributeError lands in the catch
            # below and every image is logged as "could not fetch", which reads as a
            # network problem rather than a client that was never authenticated.
            logger.info("page_figures: no authenticated session; %s not fetched", src)
            return None
        if src.startswith("data:"):
            # Its payload is the picture. One we cannot decode is not something a
            # request can recover — urlparse calls "data" a scheme, so without this
            # the src is handed to the session as if it were a URL.
            return None
        url = src if urlparse(src).scheme else urljoin(base_url, src)
        # One dead URL is embedded on many pages, and each retry costs a round trip
        # to learn the same thing. Remembered for the run, not across runs: a 404
        # today can be a real image tomorrow.
        if failed is not None and url in failed:
            return None
        try:
            # `with`, because stream=True holds the connection out of the pool until the
            # body is read or the response closed — and most paths here return without
            # reading one. A scrape walks thousands of images; leaking a connection per
            # refused image exhausts the pool and stalls the sync.
            with session.get(url, stream=True, timeout=20) as response:
                if response.status_code != 200:
                    logger.info("page_figures: %s returned %s; not described", url, response.status_code)
                    if failed is not None:
                        failed.add(url)
                    return None
                declared = (response.headers.get("Content-Type") or "").split(";")[0].strip().lower()
                if declared and not declared.startswith("image/"):
                    return None
                if declared and declared not in _IMAGE_TYPES:
                    return None
                # A header that is not a number tells us nothing about the size; the
                # read below is capped regardless. Letting it raise would skip the
                # image, log it as a fetch failure and poison the run's memo for a URL
                # that was never actually unreachable.
                declared_length = _declared_length(response.headers.get("Content-Length"))
                if declared_length is not None and declared_length > MAX_IMAGE_BYTES:
                    logger.info("page_figures: %s is %s bytes, over the cap; not described", url, declared_length)
                    return None
                # Read with a ceiling even when the server declared no length: a wrong or
                # absent Content-Length must not become an unbounded read.
                data = _read_capped(response)
                if data is None:
                    logger.info("page_figures: %s exceeded the byte cap while reading; not described", url)
                return data
        except Exception as exc:
            logger.info("page_figures: could not fetch %s: %s", url, exc)
            if failed is not None:
                failed.add(url)
            return None

    return fetch


def collect_page_figures(html: str, fetch: Callable[[str], Optional[bytes]]) -> List[Dict[str, Any]]:
    """Downloaded, deduplicated images from one page, ready for ``describe_figures``."""
    figures: List[Dict[str, Any]] = []
    by_hash = set()
    for index, found in enumerate(page_image_sources(html), start=1):
        data = fetch(found["src"])
        if not data or len(data) < MIN_IMAGE_BYTES:
            continue
        mime_type = _sniff_mime(data)
        if mime_type is None:
            # Not an image this provider can read, whatever the page called it.
            continue
        digest = hashlib.sha256(data).hexdigest()
        if digest in by_hash:
            continue
        by_hash.add(digest)
        figures.append(
            {
                "index": len(figures) + 1,
                "content_hash": digest,
                "mime_type": mime_type,
                "data": base64.b64encode(data).decode("ascii"),
                "alt": found["alt"],
                "src": found["src"],
            }
        )
    return figures


def figure_chunk_text(title: str, url: str, figure: Dict[str, Any]) -> Optional[str]:
    """One figure's chunk, anchored to the page that carries it.

    A chunk that only says "there is a picture here" is never indexed — it would
    retrieve for anything and answer nothing.
    """
    summary = (figure.get("summary") or "").strip()
    transcription = (figure.get("transcription") or "").strip()
    if not summary and not transcription:
        return None
    parts = [f"{title or url or 'page'} — figure {figure.get('index', 1)}\n"]
    alt = (figure.get("alt") or "").strip()
    if alt:
        parts.append(f"\nCaption: {alt}\n")
    if summary:
        parts.append(f"\nFigure: {summary}\n")
    if transcription:
        parts.append(f"\nText in figure:\n{transcription}\n")
    return "".join(parts)


def confluence_attachment_fetcher(confluence, page_id: str, base_url: str) -> Callable[[str], Optional[bytes]]:
    """Fetch a page's images through its attachment records, not the rendered src.

    Confluence Cloud serves ``/download/attachments/...`` — the URL its own HTML
    carries — only to a browser session: an API token gets 401 there. The REST
    attachment record exposes a ``_links.download`` the same token can read, so an
    ``<img>`` is matched back to its attachment by filename.

    Only attachments and inline data URIs are fetched. An ``<img>`` pointing
    anywhere else — an absolute URL pasted in from another site — is skipped without
    a request: it is someone else's asset, never ours to authenticate against, and
    asking costs a round trip per page to learn the same thing.
    """
    session = getattr(confluence, "_session", None)
    # The client's own url, not the configured host: Cloud is served under /wiki and
    # Data Center under whatever context path the instance uses, and build_confluence_client
    # has already normalised both. Joining against the raw host loses that prefix.
    root = (getattr(confluence, "url", "") or base_url or "").rstrip("/")

    # Listed on first use, not here: a fetcher is built for every page walked, and
    # most pages carry no image at all. Listing eagerly spent one Confluence API call
    # per page of the space to discover there was nothing to fetch.
    links: Dict[str, str] = {}
    listed = False

    def attachments() -> Dict[str, str]:
        nonlocal listed
        if listed:
            return links
        listed = True
        try:
            listing = confluence.get_attachments_from_content(page_id=page_id) or {}
            for attachment in listing.get("results") or []:
                name = (attachment.get("title") or "").strip()
                download = ((attachment.get("_links") or {}).get("download") or "").strip()
                if name and download:
                    links[name] = download
        except Exception as exc:
            logger.info("page_figures: could not list attachments for page %s: %s", page_id, exc)
        return links

    def fetch(src: str) -> Optional[bytes]:
        inline = _decode_data_uri(src)
        if inline is not None:
            return inline

        name = unquote(urlparse(src).path.rsplit("/", 1)[-1])
        download = attachments().get(name)
        if download is None:
            # Not attached to this page. Pages carry absolute <img> URLs pasted in
            # from elsewhere, and those are someone else's asset: often dead, never
            # ours to authenticate against, and a request each to find that out.
            logger.debug("page_figures: %s is not an attachment of page %s; skipped", name, page_id)
            return None
        if session is not None:
            try:
                # See default_fetcher: stream=True must be closed on every path.
                with session.get(f"{root}/{download.lstrip('/')}", stream=True, timeout=20) as response:
                    if response.status_code == 200:
                        return _read_capped(response)
                    logger.info("page_figures: attachment %s returned %s", name, response.status_code)
            except Exception as exc:
                logger.info("page_figures: could not fetch attachment %s: %s", name, exc)
        return None

    return fetch
