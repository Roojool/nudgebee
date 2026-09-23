package logsafe

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestShape_DescribesWithoutRevealing(t *testing.T) {
	const canary = "Zx9-canary-7Qw"
	for _, v := range []any{
		canary,
		[]any{canary},
		map[string]any{"deep": canary},
		map[string]any{"a": map[string]any{"b": canary}},
		42,
		nil,
	} {
		assert.NotContains(t, Shape(v), canary, "Shape leaked a value")
	}
}

func TestShape_Wording(t *testing.T) {
	assert.Equal(t, "<string: 14 chars>", Shape("Zx9-canary-7Qw"))
	assert.Equal(t, "<list: 2 items>", Shape([]any{1, 2}))
	assert.Equal(t, "<map: 1 keys>", Shape(map[string]any{"a": 1}))
	assert.Equal(t, "<nil>", Shape(nil))
	assert.Equal(t, "<int>", Shape(42))
}
