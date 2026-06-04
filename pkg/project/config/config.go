// Package config reads and validates masterbelt.toml, the manifest whose
// presence marks a project root and whose entry key names the project's entry
// point.
//
// Parsing is pure: Parse works on bytes; Load is the thin filesystem wrapper
// over it. Finding the manifest and resolving entry against the root belong to
// pkg/project, so the diagnostics for those failures (project.config.missing,
// project.config.entry_not_found) are exported from here as thin wrappers over
// the generated constructors — the codes are owned by this package even though
// the conditions are detected a layer up.
package config

import (
	"errors"
	"os"
	"path"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/masterbelt/masterbelt/pkg/diagnostic"
)

// FileName is the manifest's file name. The directory that contains it is the
// project root.
const FileName = "masterbelt.toml"

// Config is the decoded masterbelt.toml:
//
//	name = "mygame"
//	version = "0.1.0"
//	entry = "src/main.belt"   # the entry point, relative to the root
//
// Unknown keys are ignored so future sections ([dependencies], [build]) can
// land without breaking older toolchains.
type Config struct {
	Name    string `toml:"name"`
	Version string `toml:"version"`
	Entry   string `toml:"entry"`
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
		diags.Add(invalid(err))
		return cfg, diags
	}

	switch cleaned := path.Clean(cfg.Entry); {
	case cfg.Entry == "":
		diags.Add(newMissingEntryDiagnostic(0, 0))
	case path.IsAbs(cfg.Entry):
		diags.Add(newInvalidDiagnostic(0, 0, "entry must be relative to the project root"))
	case cleaned == ".." || strings.HasPrefix(cleaned, "../"):
		diags.Add(newInvalidDiagnostic(0, 0, "entry must not escape the project root"))
	}
	return cfg, diags
}

// invalid adapts a TOML decode error to the config.invalid diagnostic,
// preserving the parser's byte position when it reports one.
func invalid(err error) diagnostic.Diagnostic {
	if perr, ok := errors.AsType[toml.ParseError](err); ok {
		return newInvalidDiagnostic(perr.Position.Start, perr.Position.Len, perr.Message)
	}
	return newInvalidDiagnostic(0, 0, err.Error())
}

// Missing reports that no masterbelt.toml exists at or above the directory the
// caller searched from. There is no manifest to anchor to, so the diagnostic
// spans nothing.
func Missing() diagnostic.Diagnostic {
	return newMissingDiagnostic(0, 0)
}

// EntryNotFound reports that entry names a file that does not exist on disk.
// The manifest does not track the value's offset, so the diagnostic spans
// nothing.
func EntryNotFound(entry string) diagnostic.Diagnostic {
	return newEntryNotFoundDiagnostic(0, 0, entry)
}
