"""Describing a figure, and what happens when the model cannot or will not."""

import pytest

from rag.core.documents.figure_describer import (
    content_hash,
    describe_figures,
    looks_like_missing_vision,
    parse_description,
)
from rag.core.types import LLMResult

TAGGED = "<summary>A latency dashboard.</summary>\n<text>p99 1536Mi RB-K8S-MEM-007</text>"


class FakeVisionLLM:
    def __init__(self, reply=TAGGED, error=None):
        self.reply = reply
        self.error = error
        self.calls = []

    def generate_with_images(self, prompt, images):
        self.calls.append(images)
        if self.error:
            raise self.error
        return LLMResult(text=self.reply, input_tokens=10, output_tokens=5)


class TextOnlyLLM:
    def generate(self, prompt):
        return LLMResult(text="")


@pytest.fixture(autouse=True)
def no_cost_writes(monkeypatch):
    """Keep the unit tests off the database — the cost write is exercised by its
    own test, not as a side effect of every other one."""
    monkeypatch.setattr("rag.core.documents.figure_describer._record_cost", lambda *a, **k: None)


def figure(data, index=1, page=1):
    return {"page": page, "index": index, "mime_type": "image/png", "data": data}


def test_parses_both_halves():
    parsed = parse_description(TAGGED)
    assert parsed["summary"] == "A latency dashboard."
    # The literal identifiers are the half a query actually matches.
    assert "RB-K8S-MEM-007" in parsed["transcription"]


def test_capitalised_tags_still_parse():
    # A model that capitalises the tags has still answered; losing the description over
    # its casing would be a silent gap on the knowledge base.
    parsed = parse_description("<Summary>A latency chart.</Summary>\n<TEXT>ERR-9</TEXT>")
    assert parsed["summary"] == "A latency chart."
    assert parsed["transcription"] == "ERR-9"


def test_none_transcription_is_not_indexed():
    # Storing the literal would make "NONE" a searchable token on every
    # wordless chart.
    parsed = parse_description("<summary>A logo.</summary>\n<text>NONE</text>")
    assert parsed["transcription"] == ""


def test_untagged_long_reply_becomes_the_summary():
    reply = "A network topology diagram showing the checkout service calling the payments API over gRPC."
    assert parse_description(reply)["summary"] == reply


def test_untagged_refusal_is_not_a_description():
    # Otherwise the refusal is indexed AND the figure counts as described, so
    # the load reports no shortfall for a figure nothing ever read.
    assert parse_description("I can't help with that.")["summary"] == ""


def test_repeated_image_is_described_once():
    llm = FakeVisionLLM()
    figures = [figure("AAAA", page=1), figure("AAAA", page=2), figure("BBBB", page=3)]

    described, available = describe_figures("acct", figures, llm=llm, use_cache=False)

    assert len(llm.calls) == 2  # two distinct images, three positions
    assert described == 3
    assert available is True
    assert figures[1]["summary"] == "A latency dashboard."


def test_provider_without_vision_degrades_visibly():
    figures = [figure("AAAA")]

    described, available = describe_figures("acct", figures, llm=TextOnlyLLM(), use_cache=False)

    assert (described, available) == (0, False)


def test_timeout_is_not_reported_as_a_missing_model():
    # A bad minute must not permanently mark an account as having no vision.
    llm = FakeVisionLLM(error=TimeoutError("read timed out"))

    described, available = describe_figures("acct", [figure("AAAA")], llm=llm, use_cache=False)

    assert described == 0
    assert available is True


def test_unsupported_modality_is_reported_as_a_missing_model():
    llm = FakeVisionLLM(error=ValueError("ValidationException: this model does not support image input"))

    described, available = describe_figures("acct", [figure("AAAA")], llm=llm, use_cache=False)

    assert (described, available) == (0, False)


def test_error_classification():
    assert looks_like_missing_vision(ValueError("ValidationException: unsupported modality"))
    assert not looks_like_missing_vision(TimeoutError("timed out"))
    assert not looks_like_missing_vision(RuntimeError("429 rate limit exceeded"))


def test_cached_figures_are_not_sent_to_the_model(monkeypatch):
    # The point of the cache: a retrigger of an unchanged document costs nothing.
    llm = FakeVisionLLM()
    figures = [figure("AAAA"), figure("BBBB", index=2)]
    stored = {}

    def fake_load(account_id, hashes, model):
        digest = content_hash("AAAA")
        return {digest: {"summary": "From an earlier load.", "transcription": "RB-1"}}

    monkeypatch.setattr("rag.core.documents.figure_cache.load", fake_load)
    monkeypatch.setattr(
        "rag.core.documents.figure_cache.store",
        lambda account_id, figs, model, parent="": stored.update({"count": len(figs)}),
    )

    described, available = describe_figures("acct", figures, llm=llm)

    # Only the uncached image reached the model.
    assert len(llm.calls) == 1
    assert figures[0]["summary"] == "From an earlier load."
    assert described == 2
    assert available is True
    assert stored["count"] == 2


def test_everything_cached_skips_the_model_entirely(monkeypatch):
    llm = FakeVisionLLM()
    figures = [figure("AAAA")]
    monkeypatch.setattr(
        "rag.core.documents.figure_cache.load",
        lambda account_id, hashes, model: {content_hash("AAAA"): {"summary": "cached", "transcription": ""}},
    )
    monkeypatch.setattr("rag.core.documents.figure_cache.store", lambda *a, **k: None)

    described, available = describe_figures("acct", figures, llm=llm)

    assert llm.calls == []
    assert (described, available) == (1, True)


def test_no_usable_credential_leaves_figures_undescribed_rather_than_failing(monkeypatch, caplog):
    """A config failure is 'no vision', not 'your PDF is broken'.

    get_llm raises when an account has no usable credential — including one whose
    stored key cannot be decrypted. The caller turns an exception here into a 400
    telling the uploader their file is corrupt, so this has to degrade the way an
    image-blind provider already does.
    """
    from rag.core.documents import figure_describer

    import rag.core.embeddings.generator as generator

    def explode(_account_id):
        raise ValueError("Missing required config values for provider 'googleai': llm_provider_api_key")

    monkeypatch.setattr(generator, "get_llm", explode)

    described, vision_available = figure_describer.describe_figures(
        figures=[{"data": "iVBOR", "content_hash": "h1"}],
        account_id="acct",
    )

    assert described == 0
    assert vision_available is False
    assert any("no usable provider" in r.message for r in caplog.records)
