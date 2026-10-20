package core

import (
	"testing"

	toolcore "nudgebee/llm/tools/core"

	"github.com/stretchr/testify/assert"
)

func TestSubjectOfRequest(t *testing.T) {
	assert.Equal(t, "`nudgebee-oss/data-postgresql-0`", subjectOfRequest(toolcore.NbToolContext{
		QueryConfig: toolcore.NBQueryConfig{Namespace: "nudgebee-oss", Workload: "data-postgresql-0"},
	}))
	assert.Equal(t, "`beehive`", subjectOfRequest(toolcore.NbToolContext{
		QueryConfig: toolcore.NBQueryConfig{Workload: "beehive"},
	}))
	assert.Empty(t, subjectOfRequest(toolcore.NbToolContext{}), "a free-form chat supplies no subject")
}
