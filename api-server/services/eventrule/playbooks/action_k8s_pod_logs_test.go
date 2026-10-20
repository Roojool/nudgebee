package playbooks

import (
	"log/slog"
	"nudgebee/services/internal/testenv"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestPodLogEnricherAction(t *testing.T) {
	testenv.RequireEnv(t, testenv.Account)
	podlogEnricher := podLogAction{}
	defaultPlaybookActionContext := defaultPlaybookActionContext{
		accountId: os.Getenv("TEST_ACCOUNT"),
		logger:    slog.Default(),
		event: PlaybookEvent{
			Name:        "HighFileSystemUtilizationNbDev",
			Labels:      map[string]string{},
			Annotations: map[string]string{},
			StartedAt:   nil,
			EndedAt:     nil,
		},
	}
	response, err := podlogEnricher.Execute(&defaultPlaybookActionContext, map[string]any{
		"name":      "app-dev-85b5fbbfcf-bwfns",
		"namespace": "nudgebee",
	})
	assert.NotNil(t, response)
	assert.Nil(t, err)
}

// The empty-gzip payload below is what the agent actually returned for 258 of
// 1179 pod-log evidences on dev in the week to 2026-09-23: a successful response
// whose body decodes to nothing. Execute must treat it as "no logs" so the
// account's configured log source still gets asked — k8s_pod_log_enricher is in
// eventrule.logActions, so storing it closes the whole log category.
func TestUsableAgentLogOutput(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    bool
	}{
		{"empty gzip as stored on dev", "b'H4sIAAAAAAAA/wEAAP//AAAAAAAAAAA='", false},
		{"whitespace only", "b'H4sIAB35s2oC/1NQ4OLiBADfonUpBQAAAA=='", false},
		{"real container output", "b'H4sIAAAAAAAA/yot4EqpzMxL5wIEAAD//8XZtP0JAAAA'", true},
		{"previous-container retrieval failure", "unable to retrieve container logs for containerd://abc123", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, usableAgentLogOutput(tc.payload))
		})
	}
}

// Unanchored, the agent answers with a slice of the container's whole lifetime:
// 54 of the 239 timestamped pod-log evidences on dev (week to 2026-09-23) ended
// more than five minutes before their event, the worst 68 days before. The
// window is what ties the payload to the incident.
func TestPodLogAutoParams_AnchorsWindowToTheEvent(t *testing.T) {
	started := time.Date(2026, 9, 23, 15, 20, 0, 0, time.UTC)
	ended := started.Add(10 * time.Minute)
	event := PlaybookEvent{
		SubjectName:      "payment-worker-8646854858-ljjvj",
		SubjectNamespace: "shop",
		Labels:           map[string]string{},
		StartedAt:        &started,
		EndedAt:          &ended,
	}

	params := podLogAutoParams(event)

	// The floor extends backwards from the end, so the read covers the lead-up.
	want := int(ended.Add(-DefaultQueryWindowMinutes * time.Minute).Unix())
	assert.Equal(t, want, params["since_time"])
	assert.Equal(t, "payment-worker-8646854858-ljjvj", params["name"])
	assert.Equal(t, "shop", params["namespace"])
	assert.NotContains(t, params, "previous")
}

// A container that died hours ago must be read around its death, not around
// now, or the window excludes the very thing being investigated.
func TestPodLogAutoParams_TerminatedContainerAnchorsOnTheIncident(t *testing.T) {
	started := time.Date(2026, 9, 23, 16, 0, 0, 0, time.UTC)
	incident := time.Date(2026, 9, 23, 9, 30, 0, 0, time.UTC)
	event := PlaybookEvent{
		SubjectName:      "nb36593-crashloop-7dc8776b7f-pzcb4",
		SubjectNamespace: "demo",
		AggregationKey:   "report_crash_loop",
		Labels:           map[string]string{},
		StartedAt:        &started,
		IncidentAt:       &incident,
	}

	params := podLogAutoParams(event)

	assert.Equal(t, true, params["previous"])
	assert.Equal(t, int(incident.Add(-DefaultQueryWindowMinutes*time.Minute).Unix()), params["since_time"])
}

// Webhook events carry the namespace on labels rather than on the subject.
func TestPodLogAutoParams_NamespaceFallsBackToLabels(t *testing.T) {
	params := podLogAutoParams(PlaybookEvent{
		SubjectName: "cart-6d8f",
		Labels:      map[string]string{"namespace": "shop"},
	})

	assert.Equal(t, "shop", params["namespace"])
	assert.Greater(t, params["since_time"], 0)
}
