package lsp

import (
	"context"
	"testing"

	protocol "github.com/owenrumney/go-lsp/lsp"

	"github.com/masterbelt/masterbelt/internal/version"
)

// TestInitializeServerInfo pins that the server advertises the build's own
// version, so an editor's "about" shows what is actually running rather than a
// frozen string.
func TestInitializeServerInfo(t *testing.T) {
	res, err := NewServer().Initialize(context.Background(), &protocol.InitializeParams{})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if res.ServerInfo == nil {
		t.Fatal("Initialize returned no ServerInfo")
	}
	if res.ServerInfo.Name != "masterbelt" {
		t.Errorf("ServerInfo.Name = %q, want masterbelt", res.ServerInfo.Name)
	}
	if got, want := res.ServerInfo.Version, version.String(); got != want {
		t.Errorf("ServerInfo.Version = %q, want %q (the build's version)", got, want)
	}
	if res.ServerInfo.Version == "" {
		t.Error("ServerInfo.Version is empty")
	}
}
