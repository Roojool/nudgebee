import asyncio
import logging
import re
import uuid
from datetime import datetime, timezone

from sqlalchemy import inspect, select, text
from sqlalchemy.exc import IntegrityError, ProgrammingError
from sqlalchemy.orm import load_only
from sqlalchemy.orm.attributes import flag_modified, set_committed_value

from notifications_server.exceptions.common_exc import BeeHTTPError
from notifications_server.exceptions.exceptions import Err
from notifications_server.exceptions.notification_errors import InvalidRuleError
from notifications_server.models.db_base import BaseDB
from notifications_server.models.enums import EventCategory, EventType, EventAction, EventStatus, EventActor
from notifications_server.models.models import NotificationRules, NotificationRuleMappings
from notifications_server.services.audit import create_audit_request
from notifications_server.services.cache import Cache
from notifications_server.utils.datetime_utils import utc_now

LOG = logging.getLogger(__name__)

# PostgreSQL unique violation error code
UNIQUE_VIOLATION = "23505"

# Undefined column / undefined table. Pods of a new release can start before
# its migration has run, so the model may map a column the database lacks yet.
MISSING_SCHEMA_SQLSTATES = {"42703", "42P01"}

_warned_missing_schema = False


def _is_missing_schema(exc):
    orig = getattr(exc, "orig", None)
    return (getattr(orig, "sqlstate", None) or getattr(orig, "pgcode", None)) in MISSING_SCHEMA_SQLSTATES


async def _existing_rule_columns(session):
    result = await session.execute(
        text(
            "SELECT column_name FROM information_schema.columns "
            "WHERE table_name = :table AND table_schema = ANY (current_schemas(false))"
        ),
        {"table": NotificationRules.__tablename__},
    )
    return {row[0] for row in result}


async def _load_rules_with_existing_columns(session, tenant_id):
    """Load active rules selecting only the columns the database has. Columns the
    model maps but the database lacks are set to None on each loaded rule, so a
    newly added column must give None its pre-migration meaning in the matcher,
    never "match everything"."""
    present = await _existing_rule_columns(session)
    column_attrs = inspect(NotificationRules).column_attrs
    loadable = [attr for attr in column_attrs if attr.columns[0].name in present]
    missing = [attr.key for attr in column_attrs if attr.columns[0].name not in present]
    if not loadable:
        # The table itself does not exist yet, so there are no rules to load.
        return [], missing
    result = await session.execute(
        select(NotificationRules)
        .options(load_only(*[getattr(NotificationRules, attr.key) for attr in loadable]))
        .filter_by(tenant_id=tenant_id, is_active=True)
    )
    rules = result.scalars().all()
    for rule in rules:
        for key in missing:
            set_committed_value(rule, key, None)
    return rules, missing


def is_valid_string(s):
    pattern = r"^[A-Za-z][\w\s-]*$"
    return bool(re.match(pattern, s))


# Tenant-wide categories: their notifications are aggregated across every
# account, so there is no single account_id to scope the rule to and an
# account-scoped rule could never match (#28130).
#   daily_recap        — one nightly digest per tenant
#   optimize           — FinOps recommendations ranked tenant-wide
#   weekly_digest      — the weekly event-analysis digest, one per tenant-week
#   ai_cost            — the AI Cost Daily Report digest, one per tenant-day
TENANT_WIDE_SOURCES = ("daily_recap", "optimize", "weekly_digest", "ai_cost")

# Automation messages carry an account, but a rule may leave account_id empty
# to cover every account (the matcher treats a null account as a wildcard).
AUTOMATION_SOURCE = "automation"
ACCOUNT_OPTIONAL_SOURCES = TENANT_WIDE_SOURCES + (AUTOMATION_SOURCE,)

# The event types an automation rule may select. Kept in step with the Event*
# constants in runbook-server's notification service and the catalogue in the
# rule modal. Approval requests are absent on purpose: they are never
# suppressible, so a rule has nothing to say about them.
AUTOMATION_EVENT_TYPES = (
    "automation.task.completed",
    "automation.task.failed",
    "automation.task.progress",
    "automation.pr.created",
    "automation.pr.failed",
)


def _validate_automation_fields(fields):
    event_types = fields.get("event_types")
    if not isinstance(event_types, list) or not event_types:
        raise InvalidRuleError(Err.OS0010.value[0] % "event_types")
    unknown = [e for e in event_types if e not in AUTOMATION_EVENT_TYPES]
    if unknown:
        raise InvalidRuleError(Err.OS0011.value[0] % f"event_types: {', '.join(map(str, unknown))}")
    if fields.get("delivery_mode") == "batch":
        raise InvalidRuleError(Err.OS0011.value[0] % "delivery_mode: batch is not available for automation rules")


def validate_fields(fields, is_update=False):
    source = fields.get("source", None)
    name = fields.get("name", None)
    if not name or not is_valid_string(name):
        raise InvalidRuleError(Err.OS0011.value[0] % "name")
    if source is None:
        raise InvalidRuleError(Err.OS0010.value[0] % "source")
    if not is_update and source not in ACCOUNT_OPTIONAL_SOURCES and fields.get("account_id") is None:
        raise InvalidRuleError(Err.OS0010.value[0] % "account_id")
    if source == AUTOMATION_SOURCE:
        _validate_automation_fields(fields)
    return None


EMAIL_PATTERN = re.compile(r"^[a-zA-Z0-9._%+-]+@(?:[a-zA-Z0-9-]+\.)+[a-zA-Z]{2,}$")


def _is_valid_email(addr: str) -> bool:
    return bool(EMAIL_PATTERN.match(addr))


def _extract_emails_from_item(item) -> list[str]:
    """Extract email addresses from a string or dict item."""
    if isinstance(item, str):
        return [addr for addr in (m.strip() for m in item.split(",")) if addr]
    if isinstance(item, dict):
        return [item.get("email") or item.get("address")]
    raise InvalidRuleError(Err.OS0011.value[0] % "email channel type")


def _collect_emails_from_list(email_list: list, field_name: str) -> list[str]:
    """Collect and flatten emails from a list of items."""
    emails = []
    for mail in email_list:
        extracted = _extract_emails_from_item(mail)
        valid_emails = [e for e in extracted if e]
        if not valid_emails:
            raise InvalidRuleError(Err.OS0010.value[0] % f"{field_name} address")
        emails.extend(valid_emails)
    return emails


def _validate_email_list(email_list, field_name: str = "email"):
    """Validate a list of email items."""
    if not email_list:
        return
    emails = _collect_emails_from_list(email_list, field_name)
    for email in emails:
        if not _is_valid_email(email):
            raise InvalidRuleError(Err.OS0011.value[0] % f"{field_name} address: {email}")


def _normalize_channels_to_list(channels) -> list:
    """Normalize channels input to a list format."""
    if isinstance(channels, str):
        return [channels]
    if isinstance(channels, list):
        return channels
    raise InvalidRuleError(Err.OS0011.value[0] % "email channels format")


def validate_email_channels(channels):
    """Validate email channels in either new dict format or legacy list format."""
    # Handle new format: {"emails": [...], "exclusion_emails": [...]}
    if isinstance(channels, dict) and ("emails" in channels or "exclusion_emails" in channels):
        _validate_email_list(channels.get("emails"), "email")
        _validate_email_list(channels.get("exclusion_emails"), "exclusion email")
        return

    # Handle legacy format: array of emails or string
    channels_list = _normalize_channels_to_list(channels)
    _validate_email_list(channels_list, "email")


class NotificationRulesService:
    def __init__(self, engine):
        self.engine = engine
        self.cache = Cache()

    async def save_or_update_rule(self, **kwargs):
        rule_name = None
        async with BaseDB.async_session(self.engine)() as session:
            try:
                action = kwargs.get("action")
                if action.get("name") != "notifications_upsert_rule":
                    raise BeeHTTPError(400, Err.OS0004, [])

                rule_data = kwargs["input"]["rule"]
                sess_vars = kwargs["session_variables"]
                # Empty string falls through to the BeeHTTPError below since
                # both ids are required for the upsert.
                tenant_id = sess_vars.get("tenant_id", "")
                user_id = sess_vars.get("user_id", "")

                rule_id = rule_data.get("id")
                rule_name = rule_data.get("name")
                source = rule_data.get("source")

                fields = {k: v for k, v in rule_data.items() if hasattr(NotificationRules, k)}
                if "expires_at" in fields and isinstance(fields["expires_at"], str):
                    parsed = datetime.fromisoformat(fields["expires_at"])
                    fields["expires_at"] = (
                        parsed.astimezone(timezone.utc).replace(tzinfo=None) if parsed.tzinfo else parsed
                    )
                existing_rule = await self._get_existing_rule(session, rule_id, source, tenant_id)

                validate_fields(fields, is_update=existing_rule is not None)

                if existing_rule:
                    self._update_existing_rule(existing_rule, fields, user_id)
                    rule_id = existing_rule.id
                else:
                    rule_id = self._create_new_rule(session, fields, tenant_id, user_id, rule_data)
                    await session.flush()

                if rule_data.get("mappings") is not None:
                    await self._save_mappings_data(
                        session, rule_id, bool(existing_rule), user_id, tenant_id, rule_data["mappings"]
                    )

                # Double-invalidation: clear before commit to prevent concurrent reads
                # from re-caching stale data during the commit window
                self._invalidate_cache(tenant_id)
                await session.commit()
                self._invalidate_cache(tenant_id)
                await self._create_audit_entry(rule_id, tenant_id, user_id, fields, is_update=existing_rule is not None)

                return {"id": str(rule_id)}

            except IntegrityError as e:
                await session.rollback()
                error_msg = self._handle_integrity_error(e, rule_name)
                is_duplicate = "already exists" in error_msg or "unique constraint" in error_msg.lower()
                return {"error": error_msg, "status_code": 409 if is_duplicate else 500}

            except InvalidRuleError as e:
                LOG.warning("Invalid rule data: %s", e)
                return {"error": str(e), "status_code": 400}

            except BeeHTTPError as e:
                LOG.exception("BeeHTTPError while saving notification rule: %s", e)
                return {"error": str(e), "status_code": e.status_code}

            except Exception as e:
                if isinstance(e, ProgrammingError) and _is_missing_schema(e):
                    LOG.warning("Notification rule save attempted before its migration was applied: %s", e)
                    return {
                        "error": "Notification rules are being upgraded. Try again in a minute.",
                        "status_code": 503,
                    }
                LOG.exception("Unexpected error saving notification rule: %s", e)
                return {"error": "Unable to save or update notification rule", "status_code": 500}

    @staticmethod
    def _handle_integrity_error(e, rule_name):
        orig = getattr(e, "orig", None)
        # Check PostgreSQL error code - works with both psycopg2 (pgcode) and asyncpg (sqlstate)
        pgcode = getattr(orig, "pgcode", None) or getattr(orig, "sqlstate", None)
        if pgcode == UNIQUE_VIOLATION:
            constraint = getattr(getattr(orig, "diag", None), "constraint_name", None) or getattr(
                orig, "constraint_name", None
            )
            if constraint == "notification_rules_tenant_id_name_key":
                return f"A notification rule with the name '{rule_name}' already exists"
            return "Duplicate value violates unique constraint"
        LOG.exception("IntegrityError while saving notification rule")
        return "Unable to save notification rule, integrity constraint error"

    @staticmethod
    async def _get_existing_rule(session, rule_id, source, tenant_id):
        if source == "daily_recap":
            result = await session.execute(
                select(NotificationRules).filter_by(source=source, tenant_id=tenant_id).with_for_update()
            )
            return result.scalars().one_or_none()
        if not rule_id:
            return None
        result = await session.execute(
            select(NotificationRules).filter_by(id=rule_id, tenant_id=tenant_id).with_for_update()
        )
        return result.scalars().one_or_none()

    @staticmethod
    def _update_existing_rule(existing_rule, fields, user_id):
        nullable_fields = {
            "account_id",
            "description",
            "cluster",
            "namespace",
            "workload",
            "aggregation_key",
            "expires_at",
            "delivery_mode",
            "frequency",
            "severity",
            "event_types",
        }

        for key, value in fields.items():
            if key != "id":
                setattr(existing_rule, key, value)

        for f in nullable_fields - fields.keys():
            if hasattr(existing_rule, f):
                setattr(existing_rule, f, None)

        existing_rule.updated_by = user_id
        existing_rule.updated_at = utc_now()

    @staticmethod
    def _create_new_rule(session, fields, tenant_id, user_id, rule_data):
        rule_id = uuid.uuid4()
        rule = NotificationRules(
            id=rule_id,
            tenant_id=tenant_id,
            created_by=user_id,
            updated_by=user_id,
            created_at=utc_now(),
            updated_at=utc_now(),
            is_active=rule_data.get("is_active", True),
            **{k: v for k, v in fields.items() if k != "id"},
        )
        session.add(rule)
        return rule_id

    @staticmethod
    async def _save_mappings_data(session, rule_id, is_update, user_id, tenant_id, new_mappings):
        existing_mappings = {}
        if is_update:
            result = await session.execute(select(NotificationRuleMappings).filter_by(rule_id=rule_id))
            existing_mappings = {m.platform: m for m in result.scalars().all()}

        new_platforms = {m["platform"] for m in new_mappings}
        now = utc_now()

        for m in new_mappings:
            platform, channels = m["platform"], m["channels"]

            if channels and platform.lower() == "email":
                validate_email_channels(channels)

            existing = existing_mappings.get(platform)

            if existing:
                if existing.channels != channels:
                    LOG.info("Updating mapping for platform: %s", platform)
                    existing.channels = channels
                    flag_modified(existing, "channels")
                    existing.updated_by = user_id
                    existing.updated_at = now
            else:
                LOG.debug("Creating new mapping for platform: %s", platform)
                session.add(
                    NotificationRuleMappings(
                        id=uuid.uuid4(),
                        rule_id=rule_id,
                        platform=platform,
                        channels=channels,
                        created_at=now,
                        updated_at=now,
                        created_by=user_id,
                        updated_by=user_id,
                        tenant_id=tenant_id,
                    )
                )

        if is_update:
            for platform in existing_mappings.keys() - new_platforms:
                LOG.debug("Removing mapping for platform: %s", platform)
                await session.delete(existing_mappings[platform])

    def _invalidate_cache(self, tenant_id):
        if self.cache and self.cache.redis_client:
            self.cache.delete_cached_notification_rules(tenant_id)
            LOG.info("Cache invalidated for tenant: %s", tenant_id)

    @staticmethod
    async def _create_audit_entry(rule_id, tenant_id, user_id, fields, is_update=False, prev_state=None):
        event_type = EventType.NOTIFICATION_RULE_UPDATE.value if is_update else EventType.NOTIFICATION_RULE_CREATE.value
        event_action = EventAction.UPDATE.value if is_update else EventAction.CREATE.value
        try:
            await asyncio.to_thread(
                create_audit_request,
                user_id=user_id,
                tenant_id=tenant_id,
                account_id=None,
                event_time=utc_now(),
                event_category=EventCategory.NOTIFICATION_RULES.value,
                event_type=event_type,
                event_prev_state=prev_state,
                event_state={"id": str(rule_id)},
                event_actor=EventActor.NOTIFICATION_SERVICE.value,
                event_target="notification_rules",
                event_action=event_action,
                event_status=EventStatus.SUCCESS.value,
                event_attr=fields,
            )
            LOG.info("Audit request created for rule id: %s", rule_id)
        except Exception as e:
            LOG.error("Failed to create audit entry for rule %s: %s", rule_id, e)

    async def get_notification_rules(self, tenant_id):
        if self.cache and self.cache.redis_client:
            cached = self.cache.get_notification_rules(tenant_id)
            if cached:
                return cached

        async with BaseDB.async_session(self.engine)() as session:
            try:
                result = await session.execute(select(NotificationRules).filter_by(tenant_id=tenant_id, is_active=True))
                rules = result.scalars().all()
            except ProgrammingError as e:
                if not _is_missing_schema(e):
                    LOG.exception("Error getting notification rules for tenant %s: %s", tenant_id, e)
                    return []
                # The failed statement aborted the transaction; nothing else runs until rollback.
                await session.rollback()
                return await self._get_rules_before_migration(session, tenant_id)
            except Exception as e:
                LOG.exception("Error getting notification rules for tenant %s: %s", tenant_id, e)
                return []
            if rules and self.cache and self.cache.redis_client:
                self.cache.cache_notification_rules(tenant_id, rules)
            return rules

    @staticmethod
    async def _get_rules_before_migration(session, tenant_id):
        """Rules for the window between a release's pods starting and its migration
        running. Not cached: the next load after the migration reads the full shape."""
        global _warned_missing_schema
        try:
            rules, missing = await _load_rules_with_existing_columns(session, tenant_id)
        except Exception as e:
            LOG.exception("Error getting notification rules for tenant %s with the existing columns: %s", tenant_id, e)
            return []
        if not _warned_missing_schema:
            LOG.warning(
                "notification_rules lacks mapped columns %s; loading the existing columns until the migration runs",
                missing,
            )
            _warned_missing_schema = True
        return rules
