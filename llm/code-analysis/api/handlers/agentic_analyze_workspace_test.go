package handlers

import (
	"os"
	"path/filepath"
	"testing"
)

// The session workspace name is derived from the conversation id and lives in a
// shared temp dir, so anything else in the container can create it first. These
// tests pin what secureSessionWorkspace does when it finds something there.
func TestSecureSessionWorkspace(t *testing.T) {
	t.Run("creates a fresh workspace at 0700", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "code-analysis-conv1")

		if err := secureSessionWorkspace(path); err != nil {
			t.Fatalf("secureSessionWorkspace: %v", err)
		}

		assertMode(t, path, 0700)
	})

	t.Run("tightens a workspace that already exists too openly", func(t *testing.T) {
		// A workspace left behind by an older build, or by an attacker who got
		// there first: MkdirAll alone would leave the mode as it found it.
		path := filepath.Join(t.TempDir(), "code-analysis-conv2")
		if err := os.Mkdir(path, 0777); err != nil {
			t.Fatalf("seed workspace: %v", err)
		}
		if err := os.Chmod(path, 0777); err != nil { // defeat the process umask
			t.Fatalf("seed mode: %v", err)
		}

		if err := secureSessionWorkspace(path); err != nil {
			t.Fatalf("secureSessionWorkspace: %v", err)
		}

		assertMode(t, path, 0700)
	})

	t.Run("refuses a symlink and leaves its target untouched", func(t *testing.T) {
		root := t.TempDir()
		victim := filepath.Join(root, "victim")
		if err := os.Mkdir(victim, 0755); err != nil {
			t.Fatalf("seed victim: %v", err)
		}
		if err := os.Chmod(victim, 0755); err != nil {
			t.Fatalf("seed victim mode: %v", err)
		}
		path := filepath.Join(root, "code-analysis-conv3")
		if err := os.Symlink(victim, path); err != nil {
			t.Fatalf("plant symlink: %v", err)
		}

		if err := secureSessionWorkspace(path); err == nil {
			t.Fatal("expected an error for a workspace path that is a symlink, got nil")
		}

		// The point of the fix: chmod must not have followed the link. A 0700
		// victim here means the attacker redirected it.
		assertMode(t, victim, 0755)
	})

	t.Run("refuses a path that is not a directory", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "code-analysis-conv4")
		if err := os.WriteFile(path, []byte("not a directory"), 0600); err != nil {
			t.Fatalf("seed file: %v", err)
		}

		if err := secureSessionWorkspace(path); err == nil {
			t.Fatal("expected an error for a workspace path that is a file, got nil")
		}
	})
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("mode of %s = %#o, want %#o", path, got, want)
	}
}
