"""The description cache: what makes a retrigger free, and what invalidates it."""

from types import SimpleNamespace

import pytest
from qdrant_client import QdrantClient
from qdrant_client.models import Distance, PointStruct, VectorParams

from rag.core.documents import embedding_migration, figure_cache


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


# The tests above drive fakes, which is enough for keying and for proving a failure is
# swallowed. They cannot show that a description survives the round trip — the reason the
# cache exists — so these run against a real in-process Qdrant. The cache spent its whole
# life writing into a collection rebuilt at the embedder's dimension, every upsert
# rejected and the warning swallowed, and no fake could have caught it.


@pytest.fixture
def qdrant(monkeypatch):
    client = QdrantClient(":memory:")
    monkeypatch.setattr(figure_cache, "_client", lambda: client)
    return client


def _wrong_dimension_collection(client, points=0):
    client.create_collection(
        collection_name=figure_cache.COLLECTION_NAME,
        vectors_config=VectorParams(size=384, distance=Distance.COSINE),
    )
    for index in range(points):
        client.upsert(
            collection_name=figure_cache.COLLECTION_NAME,
            points=[PointStruct(id=index + 1, vector=[0.0] * 384, payload={"someone": "else"})],
        )


def test_a_stored_description_is_read_back(qdrant):
    figure_cache.store("acct", [{"content_hash": "h1", "summary": "a diagram", "transcription": "svc-7d9f"}], "model")

    assert figure_cache.load("acct", ["h1"], "model") == {"h1": {"summary": "a diagram", "transcription": "svc-7d9f"}}


def test_a_description_is_not_served_to_another_account_or_model(qdrant):
    figure_cache.store("acct", [{"content_hash": "h1", "summary": "a diagram"}], "model")

    assert figure_cache.load("other", ["h1"], "model") == {}
    assert figure_cache.load("acct", ["h1"], "newer-model") == {}


def test_the_cache_is_tagged_so_a_collection_sweep_can_recognise_it(qdrant):
    figure_cache.store("acct", [{"content_hash": "h1", "summary": "a diagram"}], "model")

    metadata = qdrant.get_collection(figure_cache.COLLECTION_NAME).config.metadata
    assert metadata["module"] == figure_cache.MODULE


def test_a_cache_rebuilt_at_an_embedding_dimension_is_healed(qdrant):
    _wrong_dimension_collection(qdrant)

    figure_cache.store("acct", [{"content_hash": "h1", "summary": "a diagram"}], "model")

    vectors = qdrant.get_collection(figure_cache.COLLECTION_NAME).config.params.vectors
    assert vectors.size == figure_cache._VECTOR_SIZE
    assert figure_cache.load("acct", ["h1"], "model")["h1"]["summary"] == "a diagram"


def test_a_wrong_dimension_cache_holding_points_is_left_alone(qdrant):
    # Those points were not written by this module, and dropping someone else's data to
    # fix ours is not a trade this path gets to make.
    _wrong_dimension_collection(qdrant, points=1)

    figure_cache.store("acct", [{"content_hash": "h1", "summary": "a diagram"}], "model")

    # The collection is still theirs and their point is still in it. What the store itself
    # then does is the server's business — a real Qdrant rejects the 1-dimension vector,
    # which is the failure this whole path exists to stop being silent.
    assert qdrant.get_collection(figure_cache.COLLECTION_NAME).config.params.vectors.size == 384
    survivor = qdrant.retrieve(collection_name=figure_cache.COLLECTION_NAME, ids=[1])
    assert survivor and survivor[0].payload == {"someone": "else"}


def test_the_embedding_migration_does_not_treat_the_figure_cache_as_an_index():
    # The sweep that emptied this cache reads the collection, not the module that owns it.
    described = SimpleNamespace(
        config=SimpleNamespace(
            params=SimpleNamespace(vectors=VectorParams(size=384, distance=Distance.COSINE)),
            metadata={"module": figure_cache.MODULE},
        ),
        points_count=0,
    )
    client = SimpleNamespace(get_collection=lambda name: described)

    assert embedding_migration._describe(client, figure_cache.COLLECTION_NAME) is None


def test_any_placeholder_vector_collection_is_skipped_whatever_owns_it():
    # The durable half of the rule: an embedding index is never one-dimensional, so the
    # next store built this way is excluded without anyone remembering to register it.
    described = SimpleNamespace(
        config=SimpleNamespace(
            params=SimpleNamespace(vectors=VectorParams(size=1, distance=Distance.COSINE)),
            metadata={},
        ),
        points_count=3,
    )
    client = SimpleNamespace(get_collection=lambda name: described)

    assert embedding_migration._describe(client, "something_new") is None
