"""The description cache: what makes a retrigger free, and what invalidates it."""

from rag.core.documents import figure_cache


class BrokenClient:
    def collection_exists(self, name):
        raise RuntimeError("qdrant is down")


def test_key_is_stable_for_the_same_image():
    assert figure_cache.cache_key("acct", "hash", "claude") == figure_cache.cache_key("acct", "hash", "claude")


def test_key_changes_with_model_and_prompt_version(monkeypatch):
    # A better model or a better prompt must re-describe, not serve the old
    # answer forever.
    base = figure_cache.cache_key("acct", "hash", "claude")
    assert figure_cache.cache_key("acct", "hash", "gpt") != base

    monkeypatch.setattr(figure_cache, "PROMPT_VERSION", "2")
    assert figure_cache.cache_key("acct", "hash", "claude") != base


def test_key_is_scoped_to_the_account():
    assert figure_cache.cache_key("a", "hash", "m") != figure_cache.cache_key("b", "hash", "m")


def test_lookup_failure_costs_tokens_not_the_ingest(monkeypatch):
    monkeypatch.setattr(figure_cache, "_client", lambda: BrokenClient())

    assert figure_cache.load("acct", ["hash"], "model") == {}


def test_store_failure_is_swallowed(monkeypatch):
    monkeypatch.setattr(figure_cache, "_client", lambda: BrokenClient())

    figure_cache.store("acct", [{"content_hash": "h", "summary": "s"}], "model")


def test_nothing_to_look_up_does_not_touch_qdrant(monkeypatch):
    def explode():
        raise AssertionError("must not connect")

    monkeypatch.setattr(figure_cache, "_client", explode)

    assert figure_cache.load("acct", [], "model") == {}


def test_undescribed_figures_are_not_stored(monkeypatch):
    def explode():
        raise AssertionError("must not connect")

    monkeypatch.setattr(figure_cache, "_client", explode)

    # Nothing worth remembering, so no round trip and no empty rows.
    figure_cache.store("acct", [{"content_hash": "h", "summary": "", "transcription": ""}], "model")
