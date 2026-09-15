package tools

import (
	"fmt"
	"net/url"
	"nudgebee/llm/tools/core"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLogsUIRef(t *testing.T) {
	ctx := core.NbToolContext{AccountId: "acc-1"}

	t.Run("with where-clause emits filter param", func(t *testing.T) {
		qb := core.QueryBuilder{
			Where: core.QueryWhereClause{
				And: []core.QueryWhereClause{
					{Binary: core.BinaryWhereClause{"namespace": {core.Eq: "nudgebee"}}},
					{Binary: core.BinaryWhereClause{"app": {core.Eq: "llm-server"}}},
				},
			},
		}
		ref := logsUIRef(ctx, qb)
		u, err := url.Parse(ref.Url)
		assert.NoError(t, err)
		assert.Equal(t, "monitoring/logs", u.Fragment)
		filter := u.Query().Get("filter")
		assert.NotEmpty(t, filter, "filter param must be present")
		assert.Contains(t, filter, `"_and"`)
		assert.Contains(t, filter, `"namespace"`)
		assert.Contains(t, filter, `"llm-server"`)
		assert.Empty(t, u.Query().Get("query"), "raw provider DSL must not be passed as query")
	})

	t.Run("empty where-clause emits bare tab link", func(t *testing.T) {
		ref := logsUIRef(ctx, core.QueryBuilder{})
		u, err := url.Parse(ref.Url)
		assert.NoError(t, err)
		assert.Equal(t, "monitoring/logs", u.Fragment)
		assert.Empty(t, u.Query().Get("filter"))
		assert.Empty(t, u.Query().Get("query"))
	})
}

// The priority list is what survives the 50-field cap, so it decides which spellings
// the model sees at all. It previously named only the Fluent-Bit + `.keyword`
// spellings, so on an ECS estate (kubernetes.namespace, kubernetes.pod.name) the
// genuinely queryable fields were the ones NOT promoted.
func TestCapESLabels_PromotesBothShipperConventions(t *testing.T) {
	filler := make([]string, 0, 80)
	for i := 0; i < 80; i++ {
		filler = append(filler, fmt.Sprintf("noise.field_%02d", i))
	}

	ecs := append([]string{"kubernetes.namespace", "kubernetes.pod.name", "kubernetes.container.name"}, filler...)
	got := capESLabels(ecs)
	assert.Len(t, got, maxESLabelsInPrompt)
	for _, f := range []string{"kubernetes.namespace", "kubernetes.pod.name", "kubernetes.container.name"} {
		assert.Contains(t, got, f, "ECS spelling must survive the cap")
	}

	fluent := append([]string{"kubernetes.namespace_name", "kubernetes.pod_name", "kubernetes.container_name"}, filler...)
	got = capESLabels(fluent)
	for _, f := range []string{"kubernetes.namespace_name", "kubernetes.pod_name", "kubernetes.container_name"} {
		assert.Contains(t, got, f, "Fluent-Bit spelling must still survive the cap")
	}
}
