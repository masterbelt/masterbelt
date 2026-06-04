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

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/source"
	"github.com/pelletier/go-toml/v2"
)

// FileName is the manifest's file name. The directory that contains it is the
// project root.
const FileName = "masterbelt.toml"

// Config is the decoded masterbelt.toml:
//
//	entry = "src/main.belt"   # the entry point, relative to the root
//
// The schema holds exactly what the toolchain reads — nothing is declared
// ahead of a consumer. Unknown keys are ignored, so future keys and sections
// ([dependencies], [build]) can land without breaking older toolchains.
type Config struct {
	Entry string `toml:"entry"`
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

// EntryNotFound reports that entry names a file that does not exist on disk.
// The manifest does not track the value's offset, so the diagnostic spans
// nothing.
func EntryNotFound(entry string) diagnostic.Diagnostic {
	return newEntryNotFoundDiagnostic(0, 0, entry)
}
