// Package belttest holds the on-disk fixture helpers the project-shaped
// tests share: laying a tree of masterbelt source files under a temporary
// root, the way the CLI, the project layer, and the LSP all begin.
package belttest

import (
	"os"
	"path/filepath"
	"testing"
)

// WriteFile creates one file under root — parent directories included — with
// content. The path is slash-separated regardless of platform.
func WriteFile(t *testing.T, root, path, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// WriteFiles lays files (slash-separated path -> content) under a fresh
// temporary root and returns the root.
func WriteFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for path, content := range files {
		WriteFile(t, root, path, content)
	}
	return root
}
