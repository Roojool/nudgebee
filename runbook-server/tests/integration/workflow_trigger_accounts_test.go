package integration_test

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/google/uuid"
)

// FindEventTriggers expands each trigger into one rule per account it listens to.
// The registry trusts these rows, so the tenant guard and the defaults are pinned here.
func (s *IntegrationTestSuite) TestFindEventTriggers_ListenAccounts() {
	ctx := context.Background()
	db := s.testWorkflowDao.Db()
	tenant, otherTenant := uuid.NewString(), uuid.NewString()
	own, peer, foreign := uuid.NewString(), uuid.NewString(), uuid.NewString()
	for id, t := range map[string]string{own: tenant, peer: tenant, foreign: otherTenant} {
		_, err := db.ExecContext(ctx, `INSERT INTO cloud_accounts (id, tenant) VALUES ($1, $2)`, id, t)
		s.Require().NoError(err)
	}

	insert := func(status string, trigger map[string]any) string {
		id := uuid.NewString()
		def, err := json.Marshal(map[string]any{"triggers": []any{trigger}})
		s.Require().NoError(err)
		_, err = db.ExecContext(ctx, `INSERT INTO workflows (id, tenant_id, account_id, name, definition, tags, status)
			VALUES ($1, $2, $3, $4, $5, '[]', $6)`, id, tenant, own, "trigger-accounts-"+id, def, status)
		s.Require().NoError(err)
		return id
	}
	event := func(params map[string]any) map[string]any {
		params["event_type"] = "alert"
		return map[string]any{"type": "event", "params": params}
	}
	cases := map[string]struct {
		id   string
		want []string
	}{
		"no accounts listens to own":        {insert("ACTIVE", event(map[string]any{})), []string{own}},
		"duplicates collapse to one rule":   {insert("ACTIVE", event(map[string]any{"accounts": []string{own, peer, peer}})), []string{own, peer}},
		"other tenant's account is dropped": {insert("ACTIVE", event(map[string]any{"accounts": []string{foreign}})), nil},
		"inactive workflow has no rules":    {insert("INACTIVE", event(map[string]any{"accounts": []string{peer}})), nil},
		"optimization clusters listen":      {insert("ACTIVE", map[string]any{"type": "optimization", "params": map[string]any{"clusters": []string{peer}}}), []string{peer}},
		"cluster name is not an account":    {insert("ACTIVE", map[string]any{"type": "optimization", "params": map[string]any{"clusters": []string{"k8s-dev"}}}), nil},
	}

	rules, err := s.testWorkflowDao.FindEventTriggers(ctx)
	s.Require().NoError(err)
	got := map[string][]string{}
	for _, r := range rules {
		got[r.WorkflowID] = append(got[r.WorkflowID], r.ListenAccountID)
	}
	for name, tc := range cases {
		sort.Strings(got[tc.id])
		sort.Strings(tc.want)
		s.Equal(tc.want, got[tc.id], name)
	}
}
