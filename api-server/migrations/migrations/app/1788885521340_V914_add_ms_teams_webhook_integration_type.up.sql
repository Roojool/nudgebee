-- Microsoft Teams channel webhook (Power Automate). One-way delivery via a URL,
-- stored on integrations/integration_config_values like other messaging types.
-- Unlike ms_teams, many per tenant — so deliberately NO unique index on
-- integrations.name for this type.
INSERT INTO integration_categories(value, description)
VALUES ('messaging', 'Chat and messaging platforms for bot interactions and notification delivery')
ON CONFLICT (value) DO NOTHING;

INSERT INTO integration_types(name, category, description)
VALUES ('ms_teams_webhook', 'messaging',
        'Microsoft Teams channel webhook (Power Automate) for one-way notification delivery')
ON CONFLICT (name) DO NOTHING;

-- notification_rule_mappings.platform is FK'd to this lookup table.
INSERT INTO notification_platform_types(value)
VALUES ('ms_teams_webhook')
ON CONFLICT (value) DO NOTHING;
