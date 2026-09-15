from unittest.mock import MagicMock

from notifications_server.services.common import CommonService


def test_extract_text_from_blocks_includes_section_fields():
    # Section blocks with "fields" (Slack's multi-column key/value layout,
    # used e.g. by generic.py's _build_meta_fields_block for Automation/Run/
    # Requested, and by CI-report style messages for Total Tests/Failed/etc.)
    # were silently dropped: only the singular block["text"] and
    # block["elements"] were read.
    msg = {
        "text": "Execution Finished (15 Sept 2026, 11:08:39 am)",
        "blocks": [
            {"type": "header", "text": {"type": "plain_text", "text": "Execution Finished"}},
            {
                "type": "section",
                "fields": [
                    {"type": "mrkdwn", "text": "*Total Tests*\n988"},
                    {"type": "mrkdwn", "text": "*Failed*\n29"},
                ],
            },
        ],
    }

    parts = []
    CommonService._extract_text_from_blocks(msg, parts)

    assert "*Total Tests*\n988" in parts
    assert "*Failed*\n29" in parts
    assert "Execution Finished" in parts


def test_extract_text_from_blocks_ignores_missing_fields():
    msg = {"blocks": [{"type": "section", "text": {"type": "mrkdwn", "text": "plain body"}}]}

    parts = []
    CommonService._extract_text_from_blocks(msg, parts)

    assert parts == ["plain body"]


def test_extract_text_from_attachments_includes_legacy_fields():
    # Third-party Slack apps (e.g. CI bots) commonly post via the legacy
    # "attachments" format, where structured data lives in fields[] as
    # {"title": ..., "value": ...} pairs — a different shape than Block
    # Kit's blocks[].fields ({"type": "mrkdwn", "text": ...}).
    msg = {
        "attachments": [
            {
                "pretext": ":warning: *Test Skipped Alert*",
                "fallback": ":warning: **Test Skipped Alert**\n[no preview available]",
                "fields": [
                    {"title": "Test Case", "value": "Enable SSH integration", "short": True},
                    {"title": "Suite", "value": "> chromium > admin/Integrations/SSH.spec.ts", "short": True},
                ],
            }
        ]
    }

    parts = []
    CommonService._extract_text_from_attachments(msg, parts)

    assert "Test Case: Enable SSH integration" in parts
    assert "Suite: > chromium > admin/Integrations/SSH.spec.ts" in parts


def test_extract_text_from_attachments_includes_nested_attachment_blocks():
    # Real payload from a third-party CI bot (see nudgebee "Test Skipped
    # Alert" reports): the attachment carries a legacy "color" sidebar but
    # puts its actual content in a nested blocks[] array, not in "fields" or
    # top-level "text"/"title". Previously this was completely unread, so
    # only the attachment's generic "fallback" string reached the LLM.
    msg = {
        "text": ":warning: *Test Skipped Alert*",
        "attachments": [
            {
                "id": 1,
                "blocks": [
                    {
                        "type": "section",
                        "text": {
                            "type": "mrkdwn",
                            "text": "*Test Case:* Disable SSH integration\n*Suite:* &gt; chromium",
                        },
                    }
                ],
                "color": "#FFA500",
                "fallback": "[no preview available]",
            }
        ],
    }

    parts = []
    CommonService._extract_text_from_attachments(msg, parts)

    assert "*Test Case:* Disable SSH integration\n*Suite:* &gt; chromium" in parts
    assert "[no preview available]" not in parts


def test_should_skip_message_skips_working_confirmation():
    # The "Got it! Working with <account>. View in Nudgebee" placeholder
    # (events.py's confirmation_message) is UX scaffolding posted immediately
    # on mention, not conversation content — it shouldn't reach the LLM as
    # context, even on the first read of a thread.
    bot = MagicMock()
    bot.bot_id = "B_OWN_BOT"
    msg = {
        "ts": "123.456",
        "bot_id": "B_OWN_BOT",
        "text": "Got it! Working with *k8s-dev*. <http://example.com|View in Nudgebee>",
    }

    assert CommonService._should_skip_message(msg, already_sent_messages=set(), bot=bot, is_first_read=True) is True


def test_should_skip_message_keeps_other_bot_text_on_first_read():
    bot = MagicMock()
    bot.bot_id = "B_OWN_BOT"
    msg = {"ts": "123.456", "bot_id": "B_OWN_BOT", "text": "Got it done, here are the results."}

    assert CommonService._should_skip_message(msg, already_sent_messages=set(), bot=bot, is_first_read=True) is False


def test_extract_meaningful_text_skips_redundant_top_level_text():
    # Real payload from a third-party CI bot ("Test Failed Alert"): Slack
    # auto-populates the top-level "text" as a flattened fallback of the
    # top-level "blocks" ("*Test Failed Alert* *Test Case:* ..." mirrors two
    # separate blocks with the same content). Appending both duplicated the
    # title/test-case line in the context sent to the LLM.
    msg = {
        "text": ":rotating_light: *Test Failed Alert* *Test Case:* Add GCP Account Integration",
        "blocks": [
            {"type": "section", "text": {"type": "mrkdwn", "text": ":rotating_light: *Test Failed Alert*"}},
            {"type": "section", "text": {"type": "mrkdwn", "text": "*Test Case:* Add GCP Account Integration"}},
        ],
    }

    bot = MagicMock()
    bot.bot_id = "other"
    service = CommonService(engine=MagicMock(), slack_app=MagicMock(), teams_app=MagicMock())
    result = service._extract_meaningful_text(msg, bot)

    assert result.count("Test Failed Alert") == 1
    assert result.count("Add GCP Account Integration") == 1


def test_extract_meaningful_text_orders_top_level_blocks_before_attachments():
    # Real payload from a third-party CI bot: the title/test-case lives in
    # msg["blocks"] while the body (File/Env/Tenant/error) lives in the
    # attachment. Slack renders the top-level blocks above the attachment, so
    # the extracted text must preserve that order (title first).
    msg = {
        "blocks": [
            {"type": "section", "text": {"type": "mrkdwn", "text": ":rotating_light: *Test Failed Alert*"}},
            {"type": "section", "text": {"type": "mrkdwn", "text": "*Test Case:* User Groups - open a group"}},
        ],
        "attachments": [
            {
                "id": 1,
                "blocks": [
                    {
                        "type": "section",
                        "text": {"type": "mrkdwn", "text": "*File:* `tests/admin/groups/CreateGroups.spec.ts:259`"},
                    }
                ],
                "color": "#ff0000",
            }
        ],
    }

    bot = MagicMock()
    bot.bot_id = "other"
    service = CommonService(engine=MagicMock(), slack_app=MagicMock(), teams_app=MagicMock())
    result = service._extract_meaningful_text(msg, bot)

    assert result.index("Test Failed Alert") < result.index("File:")


def test_extract_meaningful_text_parses_rich_text_header():
    # Real payload ("API Validation Failed"): the top-level "blocks" uses
    # Slack's "rich_text" block type (nested rich_text_section -> elements of
    # emoji/text runs), not "section". The old block extraction only looked
    # one level deep and silently produced nothing for it, and since "text"
    # was unconditionally skipped whenever "blocks" was present, the header
    # was dropped entirely rather than falling back to "text".
    msg = {
        "text": "*:rotating_light:API Validation Failed*\n*Test:* ServiceNow - Test Connection",
        "blocks": [
            {
                "type": "rich_text",
                "elements": [
                    {
                        "type": "rich_text_section",
                        "elements": [
                            {"type": "emoji", "name": "rotating_light"},
                            {"type": "text", "text": "API Validation Failed"},
                            {"type": "text", "text": "\n"},
                            {"type": "text", "text": "Test:"},
                            {"type": "text", "text": " ServiceNow - Test Connection"},
                        ],
                    }
                ],
            }
        ],
        "attachments": [
            {
                "blocks": [
                    {"type": "section", "text": {"type": "mrkdwn", "text": "*Env:* iteration-test"}},
                ],
                "fallback": "[no preview available]",
            }
        ],
    }

    bot = MagicMock()
    bot.bot_id = "other"
    service = CommonService(engine=MagicMock(), slack_app=MagicMock(), teams_app=MagicMock())
    result = service._extract_meaningful_text(msg, bot)

    assert "API Validation Failed" in result
    assert "ServiceNow - Test Connection" in result
    assert result.index("API Validation Failed") < result.index("Env:")


def test_extract_meaningful_text_falls_back_to_text_when_blocks_empty():
    # Blocks present but unparseable (e.g. an unsupported/future block type)
    # must not silently drop the message — "text" is the safety net.
    msg = {"text": "hello from a human", "blocks": [{"type": "some_future_block_type"}]}

    bot = MagicMock()
    bot.bot_id = "other"
    service = CommonService(engine=MagicMock(), slack_app=MagicMock(), teams_app=MagicMock())
    result = service._extract_meaningful_text(msg, bot)

    assert result == "hello from a human"


def test_extract_text_from_rich_text_block_numbers_ordered_lists():
    block = {
        "type": "rich_text",
        "elements": [
            {
                "type": "rich_text_list",
                "style": "ordered",
                "elements": [
                    {"type": "rich_text_section", "elements": [{"type": "text", "text": "first"}]},
                    {"type": "rich_text_section", "elements": [{"type": "text", "text": "second"}]},
                ],
            }
        ],
    }

    parts = []
    CommonService._extract_text_from_rich_text_block(block, parts)

    assert parts == ["1. first\n2. second"]


def test_extract_text_from_rich_text_block_bullets_unordered_lists():
    block = {
        "type": "rich_text",
        "elements": [
            {
                "type": "rich_text_list",
                "style": "bullet",
                "elements": [{"type": "rich_text_section", "elements": [{"type": "text", "text": "item"}]}],
            }
        ],
    }

    parts = []
    CommonService._extract_text_from_rich_text_block(block, parts)

    assert parts == ["- item"]


def test_extract_text_from_rich_text_block_formats_quote_and_preformatted():
    block = {
        "type": "rich_text",
        "elements": [
            {"type": "rich_text_quote", "elements": [{"type": "text", "text": "quoted"}]},
            {"type": "rich_text_preformatted", "elements": [{"type": "text", "text": "code"}]},
        ],
    }

    parts = []
    CommonService._extract_text_from_rich_text_block(block, parts)

    assert parts == ["> quoted\n```\ncode\n```"]
