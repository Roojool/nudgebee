package eventrule

import (
	"nudgebee/services/internal/testenv"
	"nudgebee/services/security"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTemplate(t *testing.T) {
	testenv.RequireEnv(t, testenv.Tenant, testenv.User)
	ctxt := security.NewRequestContextForUserTenant(os.Getenv("TEST_USER"), os.Getenv("TEST_TENANT"), nil, nil, nil)
	err := LoadEventActions(ctxt)
	if err != nil {
		println(err)
		t.Errorf("Test case 1 failed")
	}
}

func TestListAction(t *testing.T) {
	testenv.RequireEnv(t, testenv.Tenant, testenv.Account, testenv.User)
	ctxt := security.NewRequestContextForUserTenant(os.Getenv("TEST_USER"), os.Getenv("TEST_TENANT"), nil, nil, nil)

	listActionRequest := ListActionsRequest{CloudAccountId: os.Getenv("TEST_ACCOUNT"), Query: "sum(container_memory_usage_bytes{pod!=\"\"}) by (pod, namespace)"}

	// The label probe is injected, so this no longer needs a reachable Prometheus:
	// it asserts the narrowing behaviour that the labels drive, and that the probe
	// is asked for the cheap topk form first.
	var asked []string
	probe := func(_ *security.RequestContext, accountID, query string) ([]string, error) {
		asked = append(asked, query)
		return []string{"pod", "namespace"}, nil
	}

	actions, err := ListAction(ctxt, listActionRequest, probe)
	assert.Nil(t, err)
	assert.NotNil(t, actions)
	assert.Equal(t, []string{"topk(1, " + listActionRequest.Query + ")"}, asked,
		"a probe that answers on the first try must not be called again with the bare query")

	for _, action := range actions {
		if action.Category != "All" && action.Category != "Pod" {
			assert.Fail(t, "Test case 1 failed")
		}
	}
}

// A probe that cannot answer must leave the action list narrowed to
// nudgebee-source actions rather than failing the request: that fallback is the
// only thing standing between an unreachable metrics backend and a 500 on the
// alert screen.
func TestListActionFallsBackWhenTheProbeCannotAnswer(t *testing.T) {
	testenv.RequireEnv(t, testenv.Tenant, testenv.Account, testenv.User)
	ctxt := security.NewRequestContextForUserTenant(os.Getenv("TEST_USER"), os.Getenv("TEST_TENANT"), nil, nil, nil)
	request := ListActionsRequest{CloudAccountId: os.Getenv("TEST_ACCOUNT"), Query: "up"}

	calls := 0
	failing := func(*security.RequestContext, string, string) ([]string, error) {
		calls++
		return nil, assert.AnError
	}

	actions, err := ListAction(ctxt, request, failing)
	assert.Nil(t, err, "a probe failure must not fail the request")
	assert.NotNil(t, actions)
	assert.Equal(t, 2, calls, "the bare query is tried once after the topk form fails")
	for _, action := range actions {
		assert.Equal(t, "nudgebee", action.Source,
			"without labels the list must collapse to nudgebee-source actions")
	}
}
