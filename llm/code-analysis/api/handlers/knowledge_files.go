package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// KnowledgeFileReference is sent by llm-server after exact source validation.
// ConversationID belongs to file storage and is independent of analysis session ID.
type KnowledgeFileReference struct {
	ConversationID string `json:"conversation_id"`
	Path           string `json:"path"`
	Title          string `json:"title"`
	Source         string `json:"source"`
	Version        string `json:"version"`
	SHA256         string `json:"sha256"`
	Bytes          int64  `json:"bytes"`
	Purpose        string `json:"purpose"`
}

func (h *FileHandler) renderKnowledgeFiles(ctx context.Context, refs []KnowledgeFileReference) string {
	var out strings.Builder
	if len(refs) > 5 {
		return "\n[Knowledge unavailable: too many file references.]\n"
	}
	for i, ref := range refs {
		path, err := h.validateKnowledgeFile(ctx, ref)
		if err != nil {
			// Bound and quote unvalidated metadata, including oversized titles.
			fmt.Fprintf(&out, "\n[Knowledge file #%d %.128q unavailable or changed; do not claim it was read.]\n", i+1, ref.Title)
			continue
		}
		guidance := "Reference: supporting information; verify claims against live evidence."
		if ref.Purpose == "procedure" {
			guidance = "Procedure: check applicability and prerequisites, follow relevant steps in order. Existing permissions and approvals still apply."
		}
		fmt.Fprintf(&out, "\n%s\nKnowledge title: %q\nSource: %q\nFile: %q (%d bytes)\nThis knowledge document is available at the path above. Use your available tools to read it as needed for the task; choose how much to inspect. Do not claim unread sections were checked.\n", guidance, ref.Title, ref.Source, path, ref.Bytes)
	}
	return out.String()
}

func (h *FileHandler) validateKnowledgeFile(ctx context.Context, ref KnowledgeFileReference) (string, error) {
	if ref.ConversationID == "" || ref.Bytes < 0 || ref.Bytes > 32*1024*1024 || len(ref.SHA256) != 64 || len(ref.Title) > 1024 || len(ref.Source) > 2048 {
		return "", fmt.Errorf("invalid knowledge reference")
	}
	if _, err := hex.DecodeString(ref.SHA256); err != nil {
		return "", err
	}
	if filepath.IsAbs(ref.Path) || filepath.Clean(ref.Path) != ref.Path || !strings.HasPrefix(ref.Path, "knowledge/") {
		return "", fmt.Errorf("invalid knowledge path")
	}
	path, err := h.resolvePath(ref.Path, ref.ConversationID)
	if err != nil {
		return "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	stat, err := f.Stat()
	if err != nil || !stat.Mode().IsRegular() || stat.Size() != ref.Bytes {
		return "", fmt.Errorf("knowledge size mismatch")
	}
	// Validate the current bytes once when preparing an analysis, not on each
	// selective read. SaveFile and workspace tools can overwrite files, so a
	// cached write-time result would miss later same-size changes.
	digest := sha256.New()
	buf := make([]byte, 32*1024)
	var n int64
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		count, err := f.Read(buf)
		n += int64(count)
		if n > ref.Bytes {
			return "", fmt.Errorf("knowledge changed")
		}
		_, _ = digest.Write(buf[:count])
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	if n != ref.Bytes || hex.EncodeToString(digest.Sum(nil)) != ref.SHA256 {
		return "", fmt.Errorf("knowledge digest mismatch")
	}
	return path, nil
}
