package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Config values in a request reach plaintext only through core.ResolveKeptSecrets,
// which swaps a secret the user left masked for the saved one instead of decrypting
// what the client sent. A handler that decrypts request values itself reopens the
// decryption oracle of #39639, so no handler in this package may call a decrypt.
func TestHandlersDoNotDecryptRequestValues(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	require.NotEmpty(t, files)

	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		require.NoError(t, err)
		for _, call := range []string{"DecryptConfigValues(", "common.Decrypt("} {
			assert.NotContains(t, string(src), call,
				"%s calls %s — resolve request config values with core.ResolveKeptSecrets instead", f, call)
		}
	}
}
