"""A recommendation that raises resource requests costs money to apply.

estimated_savings carries the signed cost impact, so the notification templates
must not render a cost increase through the savings wording — "Savings: $-38.13/mo"
reads as a negative saving rather than a positive cost. Covers every platform that
renders a per-recommendation savings figure.
"""

import pytest

from notifications_server.message_templates.discord.recommendation_resolution import (
    get_discord_recommendation_resolution_template,
)
from notifications_server.message_templates.google_chat.recommendation_resolution import (
    get_gchat_recommendation_resolution_template,
)
from notifications_server.message_templates.ms_teams.recommendation_resolution import (
    get_teams_recommendation_resolution_template,
)
from notifications_server.message_templates.slack.recommendation_nudge_digest import (
    DigestRecommendation,
    format_savings_clause,
    build_posture_item_attachments,
    savings_split,
)
from notifications_server.message_templates.slack.recommendation_resolution import (
    RecommendationResolutionParams,
)


@pytest.mark.parametrize(
    "amount,expected",
    [
        (212.98, "Savings: $212.98/mo"),
        (0.0, "Savings: $0/mo"),
        (-38.13, "Costs $38.13/mo more"),
        (-1200.0, "Costs $1,200/mo more"),
    ],
)
def test_clause_wording_follows_the_direction(amount, expected):
    assert format_savings_clause(amount) == expected


def _params(savings):
    return RecommendationResolutionParams(
        recommendation_id="rec-1",
        resource_name="nudgebee-agent-prod-opentelemetry-collector",
        rule_name="pod_right_sizing",
        account_name="k8s-prod",
        severity="Critical",
        estimated_savings=savings,
        base_url="https://example.invalid",
    )


def _text(payload):
    import json

    return json.dumps(payload)


@pytest.mark.parametrize(
    "render",
    [
        get_discord_recommendation_resolution_template,
        get_gchat_recommendation_resolution_template,
        get_teams_recommendation_resolution_template,
    ],
)
def test_cost_increase_is_never_rendered_as_a_negative_saving(render):
    body = _text(render(_params(-38.13)))
    assert "$-38" not in body
    assert "-$38" not in body
    assert "38.13" in body


@pytest.mark.parametrize(
    "render",
    [
        get_discord_recommendation_resolution_template,
        get_gchat_recommendation_resolution_template,
        get_teams_recommendation_resolution_template,
    ],
)
def test_real_savings_still_render_as_savings(render):
    body = _text(render(_params(212.98)))
    assert "212.98" in body
    assert "Costs" not in body


# A mixed workload: the app container saves, the sidecar costs, and the row net
# is a few cents. The per-entry figures come from the producers' add_info stamp.
def _entry(resource, dollars):
    return {
        "resource": resource,
        "allocated": {"request": 1},
        "recommended": {"request": 2},
        "add_info": {"estimated_savings": dollars},
    }


MIXED_PAYLOAD = {
    "app": [_entry("cpu", 28.2), _entry("memory", 12.1)],
    "sidecar": [_entry("cpu", -10.0), _entry("memory", -29.93)],
}
SAVING_ONLY_PAYLOAD = {"app": [_entry("cpu", 28.2), _entry("memory", 12.1)]}
LEGACY_PAYLOAD = {"app": [{"resource": "cpu", "allocated": {"request": 1}, "recommended": {"request": 0.5}}]}


def test_split_reads_the_per_entry_figures():
    assert savings_split(MIXED_PAYLOAD) == pytest.approx((40.3, 39.93))
    assert savings_split(SAVING_ONLY_PAYLOAD) == pytest.approx((40.3, 0))
    assert savings_split(LEGACY_PAYLOAD) is None
    assert savings_split("not a payload") is None


def test_mixed_workload_names_both_sides_instead_of_the_net():
    assert format_savings_clause(0.37, MIXED_PAYLOAD) == "Saves $40.30/mo · costs $39.93/mo more"


def test_single_direction_payload_keeps_the_net_wording():
    assert format_savings_clause(40.3, SAVING_ONLY_PAYLOAD) == "Savings: $40.30/mo"
    assert format_savings_clause(-38.13, LEGACY_PAYLOAD) == "Costs $38.13/mo more"


def test_digest_attachment_names_both_sides_for_a_mixed_workload():
    rec = DigestRecommendation(
        id="r1",
        rule_name="pod_right_sizing",
        resource_name="checkout",
        finops_score=50,
        finops_band="Medium",
        estimated_savings=0.37,
        recommendation=MIXED_PAYLOAD,
    )
    text = build_posture_item_attachments([("k8s-prod", rec)], base_url="", color="#000000")[0]["text"]
    assert "Saves *$40.30/mo* · costs *$39.93/mo* more" in text
    assert "Save *$0" not in text
