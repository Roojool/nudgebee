-- Slack incoming-channel webhook. One-way delivery via a URL, stored on
-- integrations/integration_config_values like other messaging types.
-- Distinct from 'slack' (OAuth bot install) - many per tenant, so deliberately
-- NO unique index on integrations.name for this type.
INSERT INTO integration_categories(value, description)
VALUES ('messaging', 'Chat and messaging platforms for bot interactions and notification delivery')
ON CONFLICT (value) DO NOTHING;

INSERT INTO integration_types(name, category, description)
VALUES ('slack_webhook', 'messaging',
        'Slack incoming channel webhook for one-way notification delivery')
ON CONFLICT (name) DO NOTHING;

-- notification_rule_mappings.platform is FK'd to this lookup table.
INSERT INTO notification_platform_types(value)
VALUES ('slack_webhook')
ON CONFLICT (value) DO NOTHING;
