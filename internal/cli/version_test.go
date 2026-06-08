package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/internal/version"
)

// stampVersion fixes a build identity for the duration of a test, so the
// command's output does not depend on whether the test binary carries VCS info.
func stampVersion(t *testing.T) {
	t.Helper()
	p, c, d := version.Patch, version.Commit, version.Date
	version.Patch, version.Commit, version.Date = "20260608", "dfbe69acc6163073", "2026-06-08T15:48:31+09:00"
	t.Cleanup(func() { version.Patch, version.Commit, version.Date = p, c, d })
}

func execVersion(t *testing.T, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	RootCmd.SetOut(&out)
	RootCmd.SetErr(&out)
	RootCmd.SetArgs(append([]string{"version"}, args...))
	t.Cleanup(func() {
		RootCmd.SetOut(nil)
		RootCmd.SetErr(nil)
		RootCmd.SetArgs([]string{})
		_ = VersionCmd.Flags().Set("format", "text")
	})
	if err := RootCmd.Execute(); err != nil {
		t.Fatalf("version = %v\n%s", err, out.String())
	}
	return out.String()
}

func TestVersionText(t *testing.T) {
	stampVersion(t)
	out := execVersion(t)
	if !strings.Contains(out, "masterbelt 0.1.20260608+dfbe69a (nightly)") {
		t.Errorf("version text = %q, want the stamped identity line", out)
	}
	for _, want := range []string{"commit:", "go:", "os/arch:"} {
		if !strings.Contains(out, want) {
			t.Errorf("version text missing %q:\n%s", want, out)
		}
	}
}

func TestVersionJSON(t *testing.T) {
	stampVersion(t)
	out := execVersion(t, "--format=json")
	var got struct {
		Version, Channel, Commit, Date, Go, OS, Arch string
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("version --format=json is not JSON: %v\n%s", err, out)
	}
	if got.Version != "0.1.20260608+dfbe69a" || got.Channel != "nightly" {
		t.Errorf("json identity = %+v", got)
	}
	if got.Commit != "dfbe69acc6163073" || got.Go == "" || got.OS == "" || got.Arch == "" {
		t.Errorf("json is missing facts: %+v", got)
	}
}
