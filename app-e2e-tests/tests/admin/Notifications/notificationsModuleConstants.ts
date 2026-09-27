// Not for OSS

// Admin > Notification Rules is reached by fragment, and /user-management redirects to the
// first enabled tab when the fragment is missing — so the fragment is part of the route.
export const NOTIFICATIONS_PATH = "/user-management#notification-rules";

// CustomTable is given id='Notifications' by app/src/components/notifications/index.tsx
// (the `NOTIFICATIONS_TABLE_ID` const). It puts that id on the <table>, `${id}-body` on the
// <tbody>, and EmptyData renders `${id}-no-data` in their place when there are no rows.
export const RULES_TABLE = "Notifications";

// Column contract from HEADERS in app/src/components/notifications/index.tsx.
// The fourth header is the empty actions column, which has no label to assert.
export const RULE_COLUMNS = ["Rule", "Covers", "Status"];

// Every rule this suite creates is a one-hour weekly-digest Don't send (see
// makeRuleInert), so its row reads as a paused weekly digest.
export const RULE_TYPE_LABEL = "Weekly digest";
export const RULE_STATUS_LABEL = /Paused/;

// Prefix every created rule so anything this suite leaks behind on the shared dev
// tenant is identifiable as test data rather than someone's real rule.
const RULE_PREFIX = "e2e-noti";

// Unique per rule, not per run: two tests creating a rule in the same millisecond
// would otherwise collide on the name's unique constraint.
let ruleCounter = 0;
export function uniqueRuleName(): string {
  ruleCounter += 1;
  return `${RULE_PREFIX} ${Date.now()} ${ruleCounter}`;
}

// The editor's refusal for a name the backend won't accept, from RuleEditorModal.tsx.
export const NAME_FORMAT_ERROR = "Rule names start with a letter and use letters, digits, spaces, _ and -.";

// Names must start with a letter, so a leading hyphen is refused while the rest of
// the string is otherwise legal — the name fails on the rule under test and nothing else.
export const INVALID_RULE_NAME = "-starts-with-a-hyphen";
