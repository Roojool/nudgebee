from notifications_server.message_templates.slack.webhook_sanitize import strip_interactive_slack_elements


def test_drops_actions_block_left_with_no_link_elements():
    payload = {
        "blocks": [
            {"type": "section", "text": {"type": "mrkdwn", "text": "hi"}},
            {"type": "actions", "elements": [{"type": "static_select", "action_id": "x"}]},
        ]
    }
    sanitized = strip_interactive_slack_elements(payload)
    assert [b["type"] for b in sanitized["blocks"]] == ["section"]


def test_keeps_url_button_drops_value_button_in_same_actions_block():
    payload = {
        "blocks": [
            {
                "type": "actions",
                "elements": [
                    {"type": "button", "text": "Open", "url": "https://x"},
                    {"type": "button", "text": "Ack", "action_id": "ack", "value": "1"},
                ],
            }
        ]
    }
    sanitized = strip_interactive_slack_elements(payload)
    elements = sanitized["blocks"][0]["elements"]
    assert len(elements) == 1
    assert elements[0]["url"] == "https://x"


def test_attachment_actions_stripped_and_callback_id_removed():
    payload = {
        "attachments": [
            {
                "callback_id": "nb_finding_actions",
                "actions": [
                    {"type": "button", "text": "View", "url": "https://x"},
                    {"type": "button", "name": "ask_nubi", "text": "Ask Nubi", "value": "{}"},
                    {"type": "select", "name": "silence_finding"},
                ],
            }
        ]
    }
    sanitized = strip_interactive_slack_elements(payload)
    attachment = sanitized["attachments"][0]
    assert "callback_id" not in attachment
    assert len(attachment["actions"]) == 1
    assert attachment["actions"][0]["url"] == "https://x"


def test_attachment_with_no_link_actions_drops_actions_key():
    payload = {"attachments": [{"actions": [{"type": "select", "name": "silence_finding"}]}]}
    sanitized = strip_interactive_slack_elements(payload)
    assert "actions" not in sanitized["attachments"][0]


def test_payload_with_no_blocks_or_attachments_passes_through():
    payload = {"text": "plain message"}
    assert strip_interactive_slack_elements(payload) == {"text": "plain message"}


def test_does_not_mutate_original_payload():
    original = {"attachments": [{"actions": [{"type": "button", "text": "Ack", "action_id": "ack"}]}]}
    strip_interactive_slack_elements(original)
    assert original["attachments"][0]["actions"] == [{"type": "button", "text": "Ack", "action_id": "ack"}]
