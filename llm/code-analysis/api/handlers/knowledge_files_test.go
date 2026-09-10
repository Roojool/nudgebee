package handlers

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nudgebee/code-analysis-agent/config"
	"nudgebee/code-analysis-agent/tools"
)

func TestKnowledgeReferenceIdentityAndIntegrity(t *testing.T) {
	cfg := &config.Config{}
	cfg.Execution.WorkspaceDir = t.TempDir()
	h := NewFileHandler(cfg)
	body := strings.Repeat("background\n", 3000) + "FINAL-CANARY\n"
	ref := KnowledgeFileReference{ConversationID: "knowledge-conversation", Path: "knowledge/document.txt", Title: "SOP", Purpose: "procedure", Bytes: int64(len(body)), SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(body)))}
	path := filepath.Join(cfg.Execution.WorkspaceDir, ref.ConversationID, ref.Path)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	out := h.renderKnowledgeFiles(context.Background(), []KnowledgeFileReference{ref})
	if !strings.Contains(out, path) || !strings.Contains(out, "Procedure:") || strings.Contains(out, "FINAL-CANARY") {
		t.Fatalf("invalid reference rendering: %s", out)
	}
	read := tools.NewFileViewTool(t.TempDir()).Execute(context.Background(), map[string]any{"file_path": path, "search_pattern": "FINAL-CANARY"})
	if read.Status == "error" || !strings.Contains(read.Observation, "FINAL-CANARY") {
		t.Fatalf("downstream read failed: %+v", read)
	}
	wrong := ref
	wrong.ConversationID = "analysis-session"
	if _, err := h.validateKnowledgeFile(context.Background(), wrong); err == nil {
		t.Fatal("wrong identity accepted")
	}
	wrong = ref
	wrong.Path = "../document.txt"
	if _, err := h.validateKnowledgeFile(context.Background(), wrong); err == nil {
		t.Fatal("traversal accepted")
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", len(body))), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := h.validateKnowledgeFile(context.Background(), ref); err == nil {
		t.Fatal("changed body accepted")
	}
}

func TestKnowledgeUnavailableReferencesAreIdentifiedAndBounded(t *testing.T) {
	h := NewFileHandler(&config.Config{})
	out := h.renderKnowledgeFiles(context.Background(), []KnowledgeFileReference{
		{Title: "Missing SOP"},
		{Title: "Other\nSOP"},
		{Title: strings.Repeat("x", 10000)},
	})
	for _, want := range []string{`file #1 "Missing SOP" unavailable`, `file #2 "Other\nSOP" unavailable`, `file #3 "`} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing reference identity %q: %s", want, out)
		}
	}
	if len(out) > 1000 {
		t.Fatalf("unvalidated title inflated output to %d bytes", len(out))
	}
}
