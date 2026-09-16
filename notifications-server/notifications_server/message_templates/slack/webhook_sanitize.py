"""Strip interactivity from a Slack message payload before it goes out an
Incoming Webhook.

Templates under message_templates/slack are built for the bot install and
carry buttons/selects that POST a block_actions payload to our interactivity
endpoint on click. A webhook target has no app behind it, so anything but a
plain `url` button (which Slack opens directly, no payload sent) would render
as a dead control. Reused by both the finding card and the generic
template-notification path.
"""

from typing import Any, Dict, List


def _link_only_elements(elements: Any) -> List[Dict[str, Any]]:
    if not isinstance(elements, list):
        return []
    return [el for el in elements if isinstance(el, dict) and el.get("url")]


def strip_interactive_slack_elements(payload: Dict[str, Any]) -> Dict[str, Any]:
    """Returns a copy of payload with non-link buttons, selects, and
    overflow menus removed from both Block Kit `blocks` actions blocks and
    legacy `attachments[].actions`. Blocks/attachments left with no actions
    after stripping are dropped entirely rather than kept empty-armed."""
    if not isinstance(payload, dict):
        return {}

    sanitized = dict(payload)

    blocks = sanitized.get("blocks")
    if isinstance(blocks, list):
        new_blocks = []
        for block in blocks:
            if isinstance(block, dict) and block.get("type") == "actions":
                elements = _link_only_elements(block.get("elements"))
                if not elements:
                    continue
                block = {**block, "elements": elements}
            new_blocks.append(block)
        sanitized["blocks"] = new_blocks

    attachments = sanitized.get("attachments")
    if isinstance(attachments, list):
        new_attachments = []
        for attachment in attachments:
            if not isinstance(attachment, dict):
                new_attachments.append(attachment)
                continue
            attachment = dict(attachment)
            # Meaningless without an interactivity endpoint to route it to.
            attachment.pop("callback_id", None)
            if "actions" in attachment:
                actions = _link_only_elements(attachment.get("actions"))
                if actions:
                    attachment["actions"] = actions
                else:
                    attachment.pop("actions", None)
            new_attachments.append(attachment)
        sanitized["attachments"] = new_attachments

    return sanitized
