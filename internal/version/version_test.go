package version

import (
	"runtime/debug"
	"testing"
)

func TestFormat(t *testing.T) {
	cases := []struct {
		patch, commit, want string
	}{
		{"", "", "0.1.0-dev"}, // nothing dates the build
		{"20260608", "dfbe69acc6163", "0.1.20260608+dfbe69a"}, // dated patch + short SHA
		{"20260608", "short", "0.1.20260608"},                 // commit shorter than the short SHA: no metadata
		{"0", "abc1234ff", "0.1.0+abc1234"},                   // a non-dated patch still formats
	}
	for _, c := range cases {
		if got := format(c.patch, c.commit); got != c.want {
			t.Errorf("format(%q, %q) = %q, want %q", c.patch, c.commit, got, c.want)
		}
	}
}

func TestChannelFor(t *testing.T) {
	cases := []struct{ patch, want string }{
		{"", "dev"},
		{"20260608", "nightly"},
		{"0", "stable"},
		{"123", "stable"},
	}
	for _, c := range cases {
		if got := channelFor(c.patch); got != c.want {
			t.Errorf("channelFor(%q) = %q, want %q", c.patch, got, c.want)
		}
	}
}

func TestPseudoVersion(t *testing.T) {
	cases := []struct {
		v                     string
		wantOK                bool
		wantPatch, wantCommit string
	}{
		{"v0.0.0-20260608064831-dfbe69acc616", true, "20260608", "dfbe69acc616"},       // base-less form
		{"v1.2.4-0.20260608064831-abcdef012345", true, "20260608", "abcdef012345"},     // release-base form
		{"v1.2.3-pre.0.20260608064831-abcdef012345", true, "20260608", "abcdef012345"}, // pre-release-base form
		{"(devel)", false, "", ""}, // an in-repo build
		{"v1.2.3", false, "", ""},  // a real tag
		{"", false, "", ""},
		{"v0.0.0-notadate-dfbe69acc616", false, "", ""}, // timestamp not digits
	}
	for _, c := range cases {
		p, commit, _, ok := pseudoVersion(c.v)
		if ok != c.wantOK || (ok && (p != c.wantPatch || commit != c.wantCommit)) {
			t.Errorf("pseudoVersion(%q) = (%q, %q, ok=%v), want (%q, %q, ok=%v)", c.v, p, commit, ok, c.wantPatch, c.wantCommit, c.wantOK)
		}
	}
}

func TestVCSStamp(t *testing.T) {
	info := &debug.BuildInfo{Settings: []debug.BuildSetting{
		{Key: "vcs.revision", Value: "dfbe69acc61630733381a7230b513c0148df6a7c"},
		{Key: "vcs.time", Value: "2026-06-08T06:48:31Z"},
		{Key: "vcs.modified", Value: "false"},
	}}
	if p, c, d, ok := vcsStamp(info); !ok || p != "20260608" || c != "dfbe69acc61630733381a7230b513c0148df6a7c" || d != "2026-06-08T06:48:31Z" {
		t.Errorf("vcsStamp = (%q, %q, %q, ok=%v)", p, c, d, ok)
	}
	if _, _, _, ok := vcsStamp(&debug.BuildInfo{}); ok {
		t.Error("vcsStamp of a BuildInfo with no VCS settings reported ok")
	}
}

// A stable release build stamps Stable with the tag it was cut from, and the
// binary then names that exact version on the stable channel — overriding the
// rolling commit-dated line even when the build still carries VCS facts.
func TestStableRelease(t *testing.T) {
	defer func(s string) { Stable = s }(Stable)
	defer func(p, c, d string) { Patch, Commit, Date = p, c, d }(Patch, Commit, Date)
	// A nightly-style override is present, but the stable stamp wins.
	Patch, Commit, Date = "20260608", "dfbe69acc6163073", "2026-06-08T15:48:31+09:00"
	Stable = "0.1.1"

	if got := String(); got != "0.1.1" {
		t.Errorf("String() = %q, want 0.1.1", got)
	}
	if got := Channel(); got != "stable" {
		t.Errorf("Channel() = %q, want stable", got)
	}
	g := Get()
	if g.Version != "0.1.1" || g.Channel != "stable" {
		t.Errorf("Get() identity = %+v, want version 0.1.1 on the stable channel", g)
	}
	// The commit and date still come from the build the release was cut from.
	if g.Commit != "dfbe69acc6163073" || g.Date == "" {
		t.Errorf("Get() dropped the build facts: %+v", g)
	}
}

// The override variables take precedence over the build info and assemble the
// whole identity — what a build with no VCS information of its own would stamp.
func TestInjectedOverride(t *testing.T) {
	defer func(p, c, d string) { Patch, Commit, Date = p, c, d }(Patch, Commit, Date)
	Patch, Commit, Date = "20260608", "dfbe69acc6163073", "2026-06-08T15:48:31+09:00"

	if got := String(); got != "0.1.20260608+dfbe69a" {
		t.Errorf("String() = %q, want 0.1.20260608+dfbe69a", got)
	}
	if got := Channel(); got != "nightly" {
		t.Errorf("Channel() = %q, want nightly", got)
	}
	g := Get()
	if g.Version != "0.1.20260608+dfbe69a" || g.Channel != "nightly" || g.Commit != "dfbe69acc6163073" || g.Date == "" {
		t.Errorf("Get() identity = %+v", g)
	}
	if g.Go == "" || g.OS == "" || g.Arch == "" {
		t.Errorf("Get() is missing runtime facts: %+v", g)
	}
}
