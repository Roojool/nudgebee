"""Each recommendation line in the daily highlight email deep-links to the tab
that shows that rule. The branch picking the link matches on rule_name, so it
has to use the spelling the api-server report sends; a stale spelling drops the
reader on the generic summary instead, and nothing about the mail looks wrong.
"""

from jinja2 import ChainableUndefined, Environment, FileSystemLoader

from notifications_server.emailer.generator import get_templates_path
from notifications_server.emailer.template_params import get_default_template

ACCOUNT_ID = "acct-1"

# Every rule the optimization report can carry, with the tab its line opens.
# Keep in step with optimizationRuleNames in the api-server report.
EXPECTED_LINKS = {
    "pod_right_sizing": "optimize/right-sizing",
    "unused_pvc": "optimize/unused-volume",
    "pv_rightsize": "optimize/pv-rightsizing",
    "replica_right_sizing": "optimize/replica-rightsizing",
    "abandoned_resource": "optimize/abandoned-resources",
    "eks_cluster_upgrade": "security/cluster-upgrade",
}


def _render(rule_name):
    # Only the recommendation block matters here, so the rest of the mail's
    # context is allowed to stay empty rather than be mocked field by field.
    env = Environment(
        loader=FileSystemLoader(get_templates_path()),
        autoescape=True,
        undefined=ChainableUndefined,
    )
    params = dict(get_default_template())
    params.update(
        base_url="https://example.test",
        insights={"data": {"cloud_accounts": [{"id": ACCOUNT_ID, "account_name": "prod"}]}},
        highlight1={
            "data": {
                "recommendation_groupings": {
                    "rows": [
                        {
                            "account_id": ACCOUNT_ID,
                            "rule_name": rule_name,
                            "category": "RightSizing",
                            "count": 3,
                            "sum_estimated_savings": 12.5,
                        }
                    ]
                }
            }
        },
    )
    return env.get_template("daily_highlight_report.html").render(**params)


def test_every_rule_links_to_its_own_tab():
    for rule_name, tab in EXPECTED_LINKS.items():
        html = _render(rule_name)
        assert f"#{tab}" in html, f"{rule_name} should link to {tab}"
        assert "#optimize/summary" not in html, f"{rule_name} fell through to the generic summary"


def test_an_unknown_rule_still_falls_back_to_the_summary():
    assert "#optimize/summary" in _render("some_rule_we_have_never_seen")
