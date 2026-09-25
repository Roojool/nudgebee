package query

import (
	"fmt"
	"strings"
)

// notificationRuleAccountScope returns the AccountScope for a notification-rules
// derived table aliased as alias. An account-scoped reader sees a rule only
// when it is scoped to accounts, has at least one, and every one of them is an
// account the reader holds; rules for all accounts stay tenant-admin only.
func notificationRuleAccountScope(alias string) func(accountIds []string, quote func(any) string) string {
	return func(accountIds []string, quote func(any) string) string {
		quoted := make([]string, 0, len(accountIds))
		for _, id := range accountIds {
			quoted = append(quoted, quote(id))
		}
		return fmt.Sprintf(
			"%[1]s.scope_mode = 'accounts'"+
				" AND EXISTS (SELECT 1 FROM notification_rule_accounts nra WHERE nra.rule_id = %[1]s.id::uuid)"+
				" AND NOT EXISTS (SELECT 1 FROM notification_rule_accounts nra WHERE nra.rule_id = %[1]s.id::uuid AND nra.account_id::text NOT IN (%[2]s))",
			alias, strings.Join(quoted, ", "),
		)
	}
}
