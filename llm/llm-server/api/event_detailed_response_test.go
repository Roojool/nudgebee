package api

import (
	"strings"
	"testing"

	"nudgebee/llm/events"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A real Step 2 answer: the investigation is already a complete report, which is
// why #38925 could retire the model pass that rewrote it.
const testInvestigation = `### 📝 Event Summary
- **Title:** Volume out of disk space
- **Resource:** persistentvolumeclaim/data-postgresql-0

### Causality Chain (Root Cause)
- **Root Cause:** TOAST storage in the events table filled the 100Gi PVC.

### 🧭 Next Steps
- Resize the PVC to 150Gi.`

// The Step 1 summary's five-section contract (see buildEventSummaryQuery) is the
// deterministic source for Historical Pattern.
const testSummary = `### What is broken
- The PVC is 81% full.

### Since when
- First seen 2026-08-23, 4 firings in the current chain.

### Priority
- P2, score 61.`

func testEvent() events.Event {
	return events.Event{
		SubjectType:      "persistentvolumeclaim",
		SubjectName:      "data-postgresql-0",
		SubjectNamespace: "nudgebee-oss",
		SubjectNode:      "gke-cluster-a-pool-1-9f2c",
		SubjectOwner:     "postgresql",
		SubjectOwnerKind: "StatefulSet",
		Source:           "prometheus",
	}
}

func TestComposeDetailedResponseWithoutInvestigationProducesNothing(t *testing.T) {
	for _, investigation := range []string{"", "   ", "\n\t\n"} {
		assert.Empty(t, composeDetailedResponse(investigation, testSummary, "code findings", testEvent()),
			"a report must never be assembled without an investigation behind it")
	}
}

func TestComposeDetailedResponseCarriesTheInvestigationVerbatim(t *testing.T) {
	got := composeDetailedResponse(testInvestigation, testSummary, "", testEvent())

	assert.True(t, strings.HasPrefix(got, testInvestigation),
		"Root Cause / Evidence / Next Steps come from the investigation and must lead the report unedited")
	assert.Contains(t, got, "**Root Cause:** TOAST storage in the events table filled the 100Gi PVC.")
}

func TestComposeDetailedResponseSectionOrder(t *testing.T) {
	got := composeDetailedResponse(testInvestigation, testSummary, "Patched the retention job in `cleanup.go`.", testEvent())

	affected := strings.Index(got, "### Affected Components")
	code := strings.Index(got, "### Code Impact")
	historical := strings.Index(got, "### Historical Pattern")

	require.Greater(t, affected, 0, "Affected Components missing")
	require.Greater(t, code, 0, "Code Impact missing")
	require.Greater(t, historical, 0, "Historical Pattern missing")
	assert.Less(t, affected, code, "Affected Components precedes Code Impact, as the retired prompt ordered them")
	assert.Less(t, code, historical, "Code Impact precedes Historical Pattern")
}

func TestComposeDetailedResponseAffectedComponentsComeFromEventColumns(t *testing.T) {
	// The investigation here does not name the resource, so the bullet carries it.
	got := composeDetailedResponse("### Causality Chain\n- Root Cause: the disk filled up.", "", "", testEvent())

	assert.Contains(t, got, "- **Resource:** `persistentvolumeclaim/data-postgresql-0`")
	assert.Contains(t, got, "- **Namespace:** `nudgebee-oss`")
	assert.Contains(t, got, "- **Node:** `gke-cluster-a-pool-1-9f2c`")
	assert.Contains(t, got, "- **Owner:** `StatefulSet/postgresql`")
	assert.Contains(t, got, "- **Source:** `prometheus`")
}

func TestComposeDetailedResponseDoesNotRestateTheResourceTheInvestigationNamed(t *testing.T) {
	// testInvestigation opens with its own Event Summary naming the PVC; repeating
	// it in a second facts block is the duplication this stage exists to avoid.
	got := composeDetailedResponse(testInvestigation, "", "", testEvent())

	assert.Equal(t, 1, strings.Count(got, "data-postgresql-0"), "the resource is named once, by the investigation")
	affected := got[strings.Index(got, "### Affected Components"):]
	assert.NotContains(t, affected, "**Resource:**", "the appended facts block must not restate it")
	assert.Contains(t, got, "- **Namespace:** `nudgebee-oss`", "facts the investigation did not carry still appear")
	assert.Contains(t, got, "- **Node:** `gke-cluster-a-pool-1-9f2c`")
}

func TestComposeDetailedResponseOmitsSectionsWithNoInput(t *testing.T) {
	got := composeDetailedResponse(testInvestigation, "", "", events.Event{})

	assert.Equal(t, testInvestigation, got,
		"an event with no columns, no code stage and no summary must add no empty headings")
}

// The retired prompt emitted "Code Impact: no code-level bugs identified" on 123
// of 338 reports, most of them events where no code stage had run at all.
func TestComposeDetailedResponseCodeImpactOnlyWhenCodeStageProducedSomething(t *testing.T) {
	for _, codeFix := range []string{"", "   ", events.ClearedAnalysisDoc} {
		got := composeDetailedResponse(testInvestigation, testSummary, codeFix, testEvent())
		assert.NotContains(t, got, "### Code Impact", "no code findings must mean no Code Impact section")
	}

	got := composeDetailedResponse(testInvestigation, testSummary, "`cleanup.go:41` never deletes TOAST rows.", testEvent())
	assert.Contains(t, got, "### Code Impact\n`cleanup.go:41` never deletes TOAST rows.")
}

func TestComposeDetailedResponseHistoricalPatternComesFromTheSummary(t *testing.T) {
	got := composeDetailedResponse(testInvestigation, testSummary, "", testEvent())
	assert.Contains(t, got, "### Historical Pattern\n- First seen 2026-08-23, 4 firings in the current chain.")
	assert.NotContains(t, got, "### What is broken", "only the firing history is carried, not the whole summary")

	withoutHistory := composeDetailedResponse(testInvestigation, "### What is broken\n- Something.", "", testEvent())
	assert.NotContains(t, withoutHistory, "### Historical Pattern")
}

func TestNamesResource(t *testing.T) {
	for _, tc := range []struct {
		name, investigation, subject string
		want                         bool
	}{
		{"named in prose", "Pod `data-postgresql-0` ran out of disk.", "data-postgresql-0", true},
		{"named at the very start", "data-postgresql-0 ran out of disk.", "data-postgresql-0", true},
		{"named at the very end", "the pod is data-postgresql-0", "data-postgresql-0", true},
		{"part of a longer resource name is not a match", "Pod `data-postgresql-0-backup` is fine.", "data-postgresql-0", false},
		// The case the substring check got wrong: a short subject name buried in
		// an ordinary English word.
		{"short name inside a word", "The application restarted after the database recovered.", "api", false},
		{"short name as its own token", "The `api` deployment restarted.", "api", true},
		{"db inside database", "The database is healthy.", "db", false},
		{"slash-qualified mention still matches", "see pod/data-postgresql-0 events", "data-postgresql-0", true},
		{"absent", "Nothing to do with it.", "data-postgresql-0", false},
		{"empty subject", "anything", "", false},
		{"blank subject", "anything", "   ", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, namesResource(tc.investigation, tc.subject))
		})
	}
}

func TestComposeDetailedResponseKeepsTheResourceWhenOnlyASubstringMatches(t *testing.T) {
	ev := testEvent()
	ev.SubjectType, ev.SubjectName = "deployment", "api"
	got := composeDetailedResponse("### Findings\n- The application crashed.", "", "", ev)

	assert.Contains(t, got, "- **Resource:** `deployment/api`",
		"`api` inside `application` is not the investigation naming the resource")
}

func TestExtractMarkdownSection(t *testing.T) {
	for _, tc := range []struct {
		name, md, heading, want string
	}{
		{"plain h3", "### Since when\n- a\n- b\n\n### Priority\n- P2", "Since when", "- a\n- b"},
		{"heading level varies", "## Since when\n- a", "Since when", "- a"},
		{"case and bold markers", "### **SINCE WHEN**\n- a", "Since when", "- a"},
		{"trailing colon", "### Since when:\n- a", "Since when", "- a"},
		{"stops at deeper heading", "### Since when\n- a\n#### Detail\n- b", "Since when", "- a"},
		{"runs to end of document", "### Since when\n- a\n- b", "Since when", "- a\n- b"},
		{
			"a heading-like line inside a code fence does not end the section",
			"### Since when\n- a\n\n```bash\n# run cleanup\n./cleanup.sh\n```\n\n### Priority\n- P2",
			"Since when",
			"- a\n\n```bash\n# run cleanup\n./cleanup.sh\n```",
		},
		{"a heading inside a code fence is not the section start", "```\n### Since when\n```\n### Priority\n- P2", "Since when", ""},
		{"absent", "### Priority\n- P2", "Since when", ""},
		{"present but empty", "### Since when\n\n### Priority\n- P2", "Since when", ""},
		{"not a heading match", "Since when\n- a", "Since when", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, extractMarkdownSection(tc.md, tc.heading))
		})
	}
}

func TestDetailedResponseFailureReason(t *testing.T) {
	assert.Equal(t, "investigation produced no findings", detailedResponseFailureReason(nil))
	assert.Equal(t, "investigation produced no findings",
		detailedResponseFailureReason(&events.EventAnalysis{Status: string(events.AnalysisStatusFailed), StatusReason: "  "}))
	assert.Equal(t, "investigation failed - The AI service is temporarily unavailable",
		detailedResponseFailureReason(&events.EventAnalysis{
			Status:       string(events.AnalysisStatusFailed),
			StatusReason: " investigation failed - The AI service is temporarily unavailable ",
		}), "the reader needs the timeout that stopped the run, not a generic message")
}
