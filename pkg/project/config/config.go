// Package config reads and validates masterbelt.toml, the manifest whose
// presence marks a project root and whose profiles name the project's entry
// points.
//
// Parsing is pure: Parse works on bytes; Load is the thin filesystem wrapper
// over it. Finding the manifest, choosing a profile, and resolving its entry
// against the root belong to pkg/project, so the diagnostics for those
// failures are exported from here as thin wrappers over the generated
// constructors — the codes are owned by this package even though the
// conditions are detected a layer up.
package config

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/source"
)

// FileName is the manifest's file name. The directory that contains it is the
// project root.
const FileName = "masterbelt.toml"

// Config is the decoded masterbelt.toml. The top-level keys form the default
// profile, and each [profile.<name>] section declares a named one:
//
//	entry = "src/main.belt"     # the default profile's entry point
//
//	[profile.editor]            # a named profile
//	entry = "src/editor.belt"
//
// The schema holds exactly what the toolchain reads — nothing is declared
// ahead of a consumer. Unknown keys are ignored, so future keys and sections
// ([dependencies], [build]) can land without breaking older toolchains.
type Config struct {
	ProfileConfig                          // the default profile: the manifest's top-level keys
	Profiles      map[string]ProfileConfig `toml:"profile"`
}

// ProfileConfig is one profile's settings: an entry point relative to the
// project root, and the per-format source settings the data layer reads.
//
//	entry = "src/main.belt"
//
//	[source.csv]                # settings for the csv format
//	basePath = "data/csv"       # where its locators resolve, under the root
//
// Source is keyed by format name — the same identifier the source grammar
// (source { csv "..." }) and the format registry use — so a new format adds a
// section without a schema change.
type ProfileConfig struct {
	Entry  string                  `toml:"entry"`
	Source map[string]SourceConfig `toml:"source"`
}

// SourceConfig is one format's source settings. BasePath is the directory the
// format's locators resolve against, relative to the project root; empty (the
// section absent or the key unset) means the root itself.
type SourceConfig struct {
	BasePath string `toml:"basePath"`
}

// Load reads and parses the manifest at path. A file that exists but cannot
// be read is as unusable as a malformed one, so the failure is reported as
// project.config.invalid; report an absent manifest with Missing instead.
func Load(path string) (Config, diagnostic.List) {
	src, err := os.ReadFile(path)
	if err != nil {
		var diags diagnostic.List
		diags.Add(newInvalidDiagnostic(0, 0, err.Error()))
		return Config{}, diags
	}
	return Parse(src)
}

// Parse decodes and validates src as the content of masterbelt.toml. It
// returns the config as decoded along with the problems found; gate on
// diagnostic.List.HasErrors before trusting the result. Where the TOML parser
// reports a position (syntax and schema errors) the diagnostic carries that
// offset into src; manifest-wide problems such as an unset entry span nothing
// (offset 0, width 0), meaning the file as a whole.
func Parse(src []byte) (Config, diagnostic.List) {
	var (
		cfg   Config
		diags diagnostic.List
	)
	if err := toml.Unmarshal(src, &cfg); err != nil {
		diags.Add(invalid(src, err))
		return cfg, diags
	}

	// The default profile may stay silent when named profiles exist — using
	// it is then the error, not declaring nothing. A named profile is an
	// explicit declaration, so one without an entry has no purpose.
	if cfg.Entry == "" && len(cfg.Profiles) == 0 {
		diags.Add(newMissingEntryDiagnostic(0, 0))
	}
	if cfg.Entry != "" {
		validateEntry(cfg.Entry, "", &diags)
	}
	validateSources(cfg.Source, "", &diags)
	for _, name := range slices.Sorted(maps.Keys(cfg.Profiles)) {
		profile := cfg.Profiles[name]
		if profile.Entry == "" {
			diags.Add(newProfileMissingEntryDiagnostic(0, 0, name))
		} else {
			validateEntry(profile.Entry, name, &diags)
		}
		validateSources(profile.Source, name, &diags)
	}
	return cfg, diags
}

// confinedPath returns the reason p breaks the root-confinement policy every
// manifest path obeys — relative to the root, not escaping it — labelled by
// what kind of path it is (an entry, a base path), or "" when it is allowed.
// An empty path is reported as allowed; what it means is the caller's (an entry
// requires one, a base path defaults to the root).
func confinedPath(p, label string) string {
	if p == "" {
		return ""
	}
	// A manifest path is meant to be portable, so a backslash is treated as a
	// separator on every platform (not only where it is the OS separator): path's
	// forward-slash rules would otherwise treat "..\\shared" as one ordinary
	// segment, slipping the check while filepath.Join resolves it outside the
	// root on Windows.
	slashed := strings.ReplaceAll(p, "\\", "/")
	switch cleaned := path.Clean(slashed); {
	case path.IsAbs(slashed):
		return label + " must be relative to the project root"
	case cleaned == ".." || strings.HasPrefix(cleaned, "../"):
		return label + " must not escape the project root"
	default:
		return ""
	}
}

// validateEntry checks one profile's entry path policy. profile is "" for the
// default profile.
func validateEntry(entry, profile string, diags *diagnostic.List) {
	problem := confinedPath(entry, "entry")
	if problem == "" {
		return
	}
	if profile != "" {
		problem = fmt.Sprintf("profile %q: %s", profile, problem)
	}
	diags.Add(newInvalidDiagnostic(0, 0, problem))
}

// validateSources checks each format's base-path policy, the same confinement
// an entry obeys. Iteration is sorted so a manifest with several offending
// sections reports them in a stable order.
func validateSources(sources map[string]SourceConfig, profile string, diags *diagnostic.List) {
	for _, format := range slices.Sorted(maps.Keys(sources)) {
		problem := confinedPath(sources[format].BasePath, "base path")
		if problem == "" {
			continue
		}
		problem = fmt.Sprintf("source %q: %s", format, problem)
		if profile != "" {
			problem = fmt.Sprintf("profile %q: %s", profile, problem)
		}
		diags.Add(newInvalidDiagnostic(0, 0, problem))
	}
}

// invalid adapts a TOML decode error to the config.invalid diagnostic. The
// parser reports a row/column for both syntax and type errors; that is
// resolved to a byte offset into src (the parser exposes no length, so the
// diagnostic spans nothing).
func invalid(src []byte, err error) diagnostic.Diagnostic {
	detail := strings.TrimPrefix(err.Error(), "toml: ")
	if derr, ok := errors.AsType[*toml.DecodeError](err); ok {
		row, col := derr.Position()
		return newInvalidDiagnostic(source.NewFile("", src).Offset(row, col), 0, detail)
	}
	return newInvalidDiagnostic(0, 0, detail)
}

// Missing reports that no masterbelt.toml exists at or above the directory the
// caller searched from. There is no manifest to anchor to, so the diagnostic
// spans nothing.
func Missing() diagnostic.Diagnostic {
	return newMissingDiagnostic(0, 0)
}

// MissingEntry reports that the default profile was asked for but the
// manifest's top-level keys do not set entry. Parse accepts such a manifest
// when named profiles exist, so this fires from whoever resolves the default
// profile (pkg/project).
func MissingEntry() diagnostic.Diagnostic {
	return newMissingEntryDiagnostic(0, 0)
}

// UnknownProfile reports that the manifest declares no [profile.<name>]
// section for the requested name.
func UnknownProfile(profile string) diagnostic.Diagnostic {
	return newUnknownProfileDiagnostic(0, 0, profile)
}

// EntryNotFound reports that the default profile's entry names a file that
// does not exist on disk. The manifest does not track the value's offset, so
// the diagnostic spans nothing.
func EntryNotFound(entry string) diagnostic.Diagnostic {
	return newEntryNotFoundDiagnostic(0, 0, entry)
}

// ProfileEntryNotFound is EntryNotFound for a named profile.
func ProfileEntryNotFound(profile, entry string) diagnostic.Diagnostic {
	return newProfileEntryNotFoundDiagnostic(0, 0, profile, entry)
}
