"""Tests for scoping a Confluence scrape to page trees instead of a space.

A page tree is walked from its root through ``get_child_pages``. Unlike the
space walk, nothing else seeds the queue, so the walk itself must find every
descendant — including those beneath a container page with no body.
"""

from types import SimpleNamespace

from rag.core.types import Document

import rag.core.documents.scraper as scraper
from rag.core.documents.scraper import (
    collect_confluence_tree_documents,
    configured_page_trees,
)


def fake_confluence(pages, children):
    """pages: id -> body html (None = unreadable); children: id -> [ids]."""

    def get_page_by_id(page_id, expand=None):
        if page_id not in pages or pages[page_id] is None:
            raise RuntimeError(f"HTTP 404 for {page_id}")
        return {
            "id": page_id,
            "title": f"Runbook {page_id}",
            "body": {"view": {"value": pages[page_id]}},
            "_links": {"base": "https://wiki.example.com", "webui": f"/pages/{page_id}"},
        }

    def get_child_pages(page_id):
        return [{"id": child} for child in children.get(page_id, [])]

    return SimpleNamespace(get_page_by_id=get_page_by_id, get_child_pages=get_child_pages)


def test_configured_page_trees_splits_and_trims():
    assert configured_page_trees({"page_trees": " 100, 200 ,,300"}) == ["100", "200", "300"]
    assert configured_page_trees({"page_trees": ""}) == []
    assert configured_page_trees({}) == []


def test_tree_walk_descends_through_empty_container_pages():
    # A container-page layout: the root and a section page have no body of
    # their own, and the runbooks live several levels beneath them.
    confluence = fake_confluence(
        pages={"root": "", "section": "", "runbook": "<p>restart the pods</p>", "deep": "<p>rotate keys</p>"},
        children={"root": ["section"], "section": ["runbook"], "runbook": ["deep"]},
    )
    stats = {"failed_pages": 0, "empty_spaces": 0, "failed_roots": 0}

    documents = collect_confluence_tree_documents(confluence, "root", stats)

    assert sorted(d.page_content for d in documents) == [
        "Title: Runbook deep\n\nrotate keys",
        "Title: Runbook runbook\n\nrestart the pods",
    ]
    assert all(d.metadata["tree_root"] == "root" for d in documents)
    assert stats == {"failed_pages": 0, "empty_spaces": 0, "failed_roots": 0}


def test_tree_walk_stays_inside_the_tree_and_visits_each_page_once():
    confluence = fake_confluence(
        pages={"a": "<p>a</p>", "b": "<p>b</p>", "c": "<p>c</p>", "outside": "<p>x</p>"},
        # b is linked from two parents; nothing points at "outside".
        children={"a": ["b", "c"], "c": ["b"]},
    )
    stats = {"failed_pages": 0, "empty_spaces": 0, "failed_roots": 0}

    documents = collect_confluence_tree_documents(confluence, "a", stats)

    assert sorted(d.metadata["page_id"] for d in documents) == ["a", "b", "c"]


def test_unreadable_root_is_reported_not_treated_as_empty():
    confluence = fake_confluence(pages={"root": None}, children={})
    stats = {"failed_pages": 0, "empty_spaces": 0, "failed_roots": 0}

    assert collect_confluence_tree_documents(confluence, "root", stats) == []
    assert stats["failed_roots"] == 1


def test_integration_with_page_trees_never_walks_spaces(monkeypatch):
    confluence = fake_confluence(
        pages={"100": "<p>Runbooks</p>", "101": "<p>child</p>"},
        children={"100": ["101"]},
    )
    monkeypatch.setattr(scraper, "build_confluence_client", lambda config: confluence)
    monkeypatch.setattr(scraper, "fetch_all_spaces", lambda c: (_ for _ in ()).throw(AssertionError("spaces walked")))
    monkeypatch.setattr(scraper, "get_all_pages_from_space", lambda *a, **k: [], raising=False)
    embedded = {}

    def process_documents(documents, embeddings, **kwargs):
        embedded["ids"] = [d.metadata["page_id"] for d in documents]
        return kwargs["collection_name"], embedded["ids"]

    results = []
    monkeypatch.setattr(scraper, "process_documents", process_documents)
    monkeypatch.setattr(scraper, "update_integration_kb_load_result", lambda *a, **k: results.append((a, k)))

    integration = {"integration_id": "int-1", "config": {"namespace": "SRE", "page_trees": "100"}}
    ids = scraper._process_integration(integration, "tenant-1", embeddings=None)

    assert sorted(ids) == ["100", "101"]
    assert results[-1][0][:2] == ("int-1", "active")


def test_integration_with_unreadable_tree_ends_in_error(monkeypatch):
    confluence = fake_confluence(pages={"100": "<p>ok</p>", "200": None}, children={})
    monkeypatch.setattr(scraper, "build_confluence_client", lambda config: confluence)
    results = []
    monkeypatch.setattr(scraper, "update_integration_kb_load_result", lambda *a, **k: results.append((a, k)))
    monkeypatch.setattr(scraper, "process_documents", lambda *a, **k: (_ for _ in ()).throw(AssertionError("embedded")))

    integration = {"integration_id": "int-1", "config": {"page_trees": "100,200"}}
    assert scraper._process_integration(integration, "tenant-1", embeddings=None) == []

    args, kwargs = results[-1]
    assert args[:2] == ("int-1", "error")
    assert "1 of 2 configured Confluence page trees could not be read" in kwargs["error_message"]


def test_page_title_reaches_the_document():
    # body.view is the body only — Confluence keeps the title as its own field.
    # It is the strongest retrieval token a runbook has ("SOP - ... Queue
    # Backlog"), and a page that does not repeat its title as a body heading had
    # none of it in the index.
    confluence = fake_confluence(pages={"root": "<p>restart the pods</p>"}, children={})
    stats = {"failed_pages": 0, "empty_spaces": 0, "failed_roots": 0}

    documents = collect_confluence_tree_documents(confluence, "root", stats)

    assert documents[0].metadata["title"] == "Runbook root"
    assert documents[0].page_content.startswith("Title: Runbook root\n\n")
    assert "restart the pods" in documents[0].page_content


def test_a_page_whose_body_did_not_render_is_counted_as_failed():
    # An unrendered body (errored macro, Forge app, DC under load) must not pass
    # as a healthy sync. A container page with an empty body still must not
    # count — holding children is its job.
    confluence = fake_confluence(pages={"root": "<p>ok</p>", "container": ""}, children={"root": ["container"]})
    stats = {"failed_pages": 0, "empty_spaces": 0, "failed_roots": 0}
    collect_confluence_tree_documents(confluence, "root", stats)
    assert stats["failed_pages"] == 0, "an intentionally empty container page is not a failure"

    def no_body(page_id, expand=None):
        return {"id": page_id, "title": "T", "body": {}, "_links": {"base": "https://w", "webui": "/p"}}

    solo = fake_confluence(pages={"root": "<p>ok</p>"}, children={})
    solo.get_page_by_id = no_body
    stats = {"failed_pages": 0, "empty_spaces": 0, "failed_roots": 0}
    collect_confluence_tree_documents(solo, "root", stats)
    assert stats["failed_pages"] == 1, "a body that never rendered must be counted"


def test_integration_kb_document_count_counts_pages_not_chunks(monkeypatch):
    confluence = fake_confluence(
        pages={"100": "<p>Runbooks</p>", "101": "<p>child</p>"},
        children={"100": ["101"]},
    )
    monkeypatch.setattr(scraper, "build_confluence_client", lambda config: confluence)

    # Stand in for chunking: three embedding units per scraped page.
    def process_documents(documents, embeddings, **kwargs):
        return kwargs["collection_name"], [f"{d.metadata['page_id']}-{i}" for d in documents for i in range(3)]

    monkeypatch.setattr(scraper, "process_documents", process_documents)
    monkeypatch.setattr(scraper.source_store, "store", lambda *a, **k: None)
    results = []
    monkeypatch.setattr(scraper, "update_integration_kb_load_result", lambda *a, **k: results.append(a))

    integration = {"integration_id": "int-1", "config": {"namespace": "SRE", "page_trees": "100"}}
    ids = scraper._process_integration(integration, "tenant-1", embeddings=None)

    assert len(ids) == 6
    assert results[-1] == ("int-1", "active", 2)


def _load_result_for(monkeypatch, figure_warning):
    """Run one Confluence integration through to its load result."""
    confluence = fake_confluence(pages={"100": "<p>Runbooks</p>"}, children={})
    monkeypatch.setattr(scraper, "build_confluence_client", lambda config: confluence)
    monkeypatch.setattr(scraper, "fetch_all_spaces", lambda c: [])
    monkeypatch.setattr(
        scraper, "process_documents", lambda documents, embeddings, **kw: (kw["collection_name"], ["100"])
    )
    monkeypatch.setattr(scraper.source_store, "store", lambda *a, **k: None)
    monkeypatch.setattr(scraper, "build_page_figure_documents", lambda *a, **k: ([], figure_warning))
    results = []
    monkeypatch.setattr(scraper, "update_integration_kb_load_result", lambda *a, **k: results.append((a, k)))

    integration = {"integration_id": "int-1", "config": {"namespace": "SRE", "page_trees": "100"}}
    scraper._process_integration(integration, "tenant-1", embeddings=None)
    return results[-1]


def test_pictures_do_not_inflate_the_document_count(monkeypatch):
    # The Docs column counts source pages. A figure is a chunk of a page, so folding
    # figures in would inflate it by however many pictures the space contains — the
    # same confusion the load-history count already had to be corrected for.
    figure = Document(page_content="Runbook — figure 1\n\nText in figure:\nCODE PURPLE", metadata={"is_figure": True})
    confluence = fake_confluence(pages={"100": "<p>Runbooks</p>"}, children={})
    monkeypatch.setattr(scraper, "build_confluence_client", lambda config: confluence)
    monkeypatch.setattr(scraper, "fetch_all_spaces", lambda c: [])
    monkeypatch.setattr(
        scraper, "process_documents", lambda documents, embeddings, **kw: (kw["collection_name"], ["100"])
    )
    monkeypatch.setattr(scraper.source_store, "store", lambda *a, **k: None)
    monkeypatch.setattr(scraper, "build_page_figure_documents", lambda *a, **k: ([figure, figure, figure], ""))
    results = []
    monkeypatch.setattr(scraper, "update_integration_kb_load_result", lambda *a, **k: results.append((a, k)))

    integration = {"integration_id": "int-1", "config": {"namespace": "SRE", "page_trees": "100"}}
    scraper._process_integration(integration, "tenant-1", embeddings=None)

    args, _ = results[-1]
    assert args[:3] == ("int-1", "active", 1)


def test_a_load_that_dropped_its_pictures_says_so(monkeypatch):
    # The pages are indexed, so the load is active — but the KB must not read as
    # complete when every picture on it was thrown away. Without this the only
    # record is a log line, and the reader is told nothing.
    shortfall = "3 page image(s) were not analysed: no vision-capable model is configured"

    args, kwargs = _load_result_for(monkeypatch, shortfall)

    assert args[:2] == ("int-1", "active")
    assert kwargs["error_message"] == shortfall


def test_a_load_with_nothing_to_report_carries_no_message(monkeypatch):
    args, kwargs = _load_result_for(monkeypatch, "")

    assert args[:2] == ("int-1", "active")
    assert not kwargs["error_message"]
