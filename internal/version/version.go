// Package version reports the build's identity: its version string, the commit
// it was built from, and that commit's date. The version is assembled from the
// major.minor line managed in this file plus a patch derived from the commit, so
// every build can say what it is without anything being stamped in by the build
// system:
//
//   - Line, the major.minor release line, is managed here and bumped by hand
//     (0.1 today; 1.0 at the first stable release).
//   - the patch (the commit date) and the commit come from Go's own build info:
//     the recorded VCS revision and time for an in-repo `go build`, or the
//     module pseudo-version for a `go install <module>@latest`.
//
// Both build-info paths derive the patch from the commit's date, so the same
// commit always yields the same version. A build with no version information at
// all — a test binary, a build from a source archive with -buildvcs=false —
// reports the dev sentinel. The Patch/Commit/Date variables are an explicit
// override (set via -ldflags -X) for such a build; nothing in the normal build
// path sets them.
package version

import (
	"runtime"
	"runtime/debug"
	"strings"
	"time"
)

// Line is the release line this source tree builds — the major.minor managed
// here, bumped by hand toward the first stable release (1.0).
const Line = "0.1"

// Patch, Commit, and Date are an explicit override, set via
// -ldflags -X github.com/masterbelt/masterbelt/internal/version.<field>=…, for a
// build that carries no VCS information of its own. They are empty in every
// ordinary build, which reads the same facts from Go's build info instead.
var (
	Patch  string // the commit date YYYYMMDD
	Commit string // the full git SHA
	Date   string // the commit date in RFC3339
)

// Stable is the semantic version a stable release build reports. The release
// workflow stamps it with the tag the build was cut from
// (-ldflags -X github.com/masterbelt/masterbelt/internal/version.Stable=X.Y.Z),
// so the binary names that exact version on the stable channel. Every other
// build — the nightly, a local `make dist` — leaves it empty and reports the
// rolling commit-dated line derived from Line instead.
var Stable string

// shortSHA is how many leading hex characters of the commit the version string
// carries as build metadata.
const shortSHA = 7

// resolve returns the build's patch, commit, and date: the override variables
// when set, otherwise the VCS stamp of an in-repo build, otherwise the module
// pseudo-version of a `go install`, otherwise all empty.
func resolve() (patch, commit, date string) {
	if Patch != "" {
		return Patch, Commit, Date
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", "", ""
	}
	if p, c, d, ok := vcsStamp(info); ok {
		return p, c, d
	}
	if p, c, d, ok := pseudoVersion(info.Main.Version); ok {
		return p, c, d
	}
	return "", "", ""
}

// vcsStamp reads the revision and commit time Go records for an in-repo build
// (go build / go install ./…). ok is false when the build carries no VCS stamp.
func vcsStamp(info *debug.BuildInfo) (patch, commit, date string, ok bool) {
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			commit = s.Value
		case "vcs.time":
			date = s.Value
		}
	}
	t, err := time.Parse(time.RFC3339, date)
	if commit == "" || err != nil {
		return "", "", "", false
	}
	return t.UTC().Format("20060102"), commit, t.UTC().Format(time.RFC3339), true
}

// pseudoVersion reads the commit date and SHA out of a module pseudo-version —
// the …-<14-digit UTC timestamp>-<12 hex SHA> Go assigns a `go install
// <module>@latest`. It covers all three pseudo-version forms (the timestamp is
// preceded by '-' for a base-less version, '.' otherwise). ok is false for any
// other module version ("(devel)", a real tag, or empty).
func pseudoVersion(v string) (patch, commit, date string, ok bool) {
	dash := strings.LastIndexByte(v, '-')
	if dash < 0 {
		return "", "", "", false
	}
	sha := v[dash+1:] // the trailing "-<sha>"
	rest := v[:dash]  // the version with that suffix removed
	if len(rest) < 15 || len(sha) < shortSHA || !isHex(sha) {
		return "", "", "", false
	}
	ts := rest[len(rest)-14:] // the 14-digit commit timestamp
	sep := rest[len(rest)-15] // the pseudo-version separator before it
	if !isDigits(ts) || (sep != '-' && sep != '.') {
		return "", "", "", false
	}
	t, err := time.Parse("20060102150405", ts)
	if err != nil {
		return "", "", "", false
	}
	return ts[:8], sha, t.UTC().Format(time.RFC3339), true
}

// format assembles the version string from the resolved parts: the line with
// its dated patch and short SHA, or the dev sentinel when nothing dates the
// build.
func format(patch, commit string) string {
	if patch == "" {
		return Line + ".0-dev"
	}
	v := Line + "." + patch
	if len(commit) >= shortSHA {
		v += "+" + commit[:shortSHA]
	}
	return v
}

// channelFor names the release channel a patch implies: no patch is a dev
// build, an 8-digit date is a rolling nightly, anything else a stable release.
func channelFor(patch string) string {
	switch {
	case patch == "":
		return "dev"
	case len(patch) == 8 && isDigits(patch):
		return "nightly"
	default:
		return "stable"
	}
}

// isDigits reports whether s is non-empty and all ASCII digits.
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// isHex reports whether s is non-empty and all lowercase hex digits.
func isHex(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// identity resolves the build's version string and channel together with the
// commit and date behind them. A stable release build (Stable stamped in) names
// that version verbatim; every other build reports the rolling line resolve
// derives from the commit.
func identity() (ver, channel, commit, date string) {
	var patch string
	patch, commit, date = resolve()
	if Stable != "" {
		return Stable, "stable", commit, date
	}
	return format(patch, commit), channelFor(patch), commit, date
}

// String returns the build's version string.
func String() string {
	ver, _, _, _ := identity()
	return ver
}

// Channel returns the build's release channel: "dev", "nightly", or "stable".
func Channel() string {
	_, channel, _, _ := identity()
	return channel
}

// Info is the full build identity, the shape `version --format=json` and any
// other machine-readable consumer emit.
type Info struct {
	Version string `json:"version"`
	Channel string `json:"channel"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
	Go      string `json:"go"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
}

// Get returns the build identity: the version facts plus the Go toolchain and
// target this binary was built for.
func Get() Info {
	ver, channel, commit, date := identity()
	return Info{
		Version: ver,
		Channel: channel,
		Commit:  commit,
		Date:    date,
		Go:      runtime.Version(),
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
	}
}
