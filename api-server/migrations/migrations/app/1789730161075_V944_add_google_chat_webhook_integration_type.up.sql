-- Google Chat incoming space webhook. One-way delivery via a URL, stored on
-- integrations/integration_config_values like other messaging types.
-- Distinct from 'google_chat_space' (service-account bound space) - many per
-- tenant, so deliberately NO unique index on integrations.name for this type.
INSERT INTO integration_categories(value, description)
VALUES ('messaging', 'Chat and messaging platforms for bot interactions and notification delivery')
ON CONFLICT (value) DO NOTHING;

INSERT INTO integration_types(name, category, description)
VALUES ('google_chat_webhook', 'messaging',
        'Google Chat incoming space webhook for one-way notification delivery')
ON CONFLICT (name) DO NOTHING;

-- notification_rule_mappings.platform is FK'd to this lookup table.
INSERT INTO notification_platform_types(value)
VALUES ('google_chat_webhook')
ON CONFLICT (value) DO NOTHING;
