"""Above FOLLOWUP_OPTIONS_THRESHOLD, `_build_followup_action_elements` used to
return only an "Open in <app>" link, hiding every option from Slack even when
the user's pick was among the first few. It should instead show a dropdown of
the first N options alongside the link, and tell the caller how many were
shown so it can warn the user more exist beyond that.
"""

from notifications_server.services import events as events_module
from notifications_server.services.events import Events


def test_over_threshold_with_account_context_returns_dropdown_and_link(monkeypatch):
    monkeypatch.setattr(events_module, "FOLLOWUP_OPTIONS_THRESHOLD", 3)
    options = ["a", "b", "c", "d", "e"]
    cached_entry = {"account_id": "acc-1", "session_id": "sess-1"}

    elements, shown = Events._build_followup_action_elements(options, cached_entry)

    assert shown == 3
    assert [el["type"] for el in elements] == ["static_select", "button"]
    dropdown_values = [opt["value"] for opt in elements[0]["options"]]
    assert dropdown_values == ["a", "b", "c"]
    assert elements[1]["action_id"] == "open_followup_in_ui"
    assert elements[1]["text"]["text"] == "See all 5 options"
    assert "acc-1" in elements[1]["url"] and "sess-1" in elements[1]["url"]


def test_over_threshold_without_account_context_falls_back_to_dropdown_only(monkeypatch):
    monkeypatch.setattr(events_module, "FOLLOWUP_OPTIONS_THRESHOLD", 3)
    options = ["a", "b", "c", "d", "e"]

    elements, shown = Events._build_followup_action_elements(options, cached_entry=None)

    assert shown is None
    assert [el["type"] for el in elements] == ["static_select"]
    dropdown_values = [opt["value"] for opt in elements[0]["options"]]
    assert dropdown_values == options


def test_small_option_list_returns_buttons_with_no_truncation_note():
    elements, shown = Events._build_followup_action_elements(["yes", "no"], cached_entry=None)

    assert shown is None
    assert [el["type"] for el in elements] == ["button", "button"]


def test_over_threshold_options_are_truncated_to_75_chars_and_deduped(monkeypatch):
    monkeypatch.setattr(events_module, "FOLLOWUP_OPTIONS_THRESHOLD", 2)
    # Both options are identical in their first 75 characters, so truncating collapses
    # them to the same value — Slack rejects a static_select with duplicate option values.
    long_a = "x" * 80 + "-a"
    long_b = "x" * 80 + "-b"
    cached_entry = {"account_id": "acc-1", "session_id": "sess-1"}

    elements, shown = Events._build_followup_action_elements([long_a, long_b, "extra"], cached_entry)

    dropdown_options = elements[0]["options"]
    assert len(dropdown_options) == 1
    assert len(dropdown_options[0]["text"]["text"]) <= 75
    assert shown == 1
    assert elements[1]["text"]["text"] == "See all 3 options"


def test_over_threshold_options_skip_none_values(monkeypatch):
    monkeypatch.setattr(events_module, "FOLLOWUP_OPTIONS_THRESHOLD", 2)
    cached_entry = {"account_id": "acc-1", "session_id": "sess-1"}

    elements, shown = Events._build_followup_action_elements(["a", None, "extra"], cached_entry)

    dropdown_values = [opt["value"] for opt in elements[0]["options"]]
    assert dropdown_values == ["a"]
    assert shown == 1
