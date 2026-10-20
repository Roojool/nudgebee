// Package contentutil holds message-content helpers shared by the provider clients under llms/.
package contentutil

import (
	"encoding/base64"

	"github.com/tmc/langchaingo/llms"
)

// ImageURLFromBinary sends image bytes as a data-URL image_url part: OpenAI-compatible servers reject
// langchaingo's own BinaryContent encoding ("type":"binary") with a 400.
func ImageURLFromBinary(b llms.BinaryContent) llms.ImageURLContent {
	return llms.ImageURLContent{URL: "data:" + b.MIMEType + ";base64," + base64.StdEncoding.EncodeToString(b.Data)}
}
