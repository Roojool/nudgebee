package azure

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tmc/langchaingo/llms"
)

func TestExtractToolParts_ImageBytesBecomeDataURLImage(t *testing.T) {
	t.Parallel()

	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0xff}
	msg := &ChatMessage{MultiContent: []llms.ContentPart{
		llms.TextContent{Text: "what is this?"},
		llms.ImageURLContent{URL: "https://example.com/a.png"},
		llms.BinaryContent{MIMEType: "image/png", Data: png},
	}}

	got, calls := ExtractToolParts(msg)

	require.Len(t, got, 3)
	assert.Empty(t, calls)
	assert.Equal(t, llms.TextContent{Text: "what is this?"}, got[0])
	assert.Equal(t, llms.ImageURLContent{URL: "https://example.com/a.png"}, got[1], "a URL image is left as it was")
	assert.Equal(t, llms.ImageURLContent{URL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)}, got[2])
}
