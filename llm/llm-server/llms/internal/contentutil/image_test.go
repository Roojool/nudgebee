package contentutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/tmc/langchaingo/llms"
)

func TestImageURLFromBinary(t *testing.T) {
	t.Parallel()

	assert.Equal(t, llms.ImageURLContent{URL: "data:image/png;base64,iVBORw=="},
		ImageURLFromBinary(llms.BinaryContent{MIMEType: "image/png", Data: []byte{0x89, 'P', 'N', 'G'}}))
	assert.Equal(t, llms.ImageURLContent{URL: "data:image/jpeg;base64,"},
		ImageURLFromBinary(llms.BinaryContent{MIMEType: "image/jpeg"}), "no bytes still yields a well-formed URL")
}
