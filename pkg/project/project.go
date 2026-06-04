// Package project models a masterbelt project: the root directory marked by
// masterbelt.toml, the parsed manifest, and the set of source files reachable
// from the entry point. In P-1 that set is exactly the entry file; multi-file
// resolution (`use`) grows it in P-2.
//
// The package depends on pkg/diagnostic and the manifest parser only — never
// on the compiler under pkg/masterbelt. The compiler's callers (the CLI today,
// the multi-file engine in P-2) are the ones that bind a Project's files to
// documents, so the dependency arrow always points from them to both.
package project

import (
	"os"
	"path"
	"path/filepath"
	"sort"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/project/config"
)

// FileID identifies a file within its project: the file's path relative to
// the project root, cleaned and "/"-separated regardless of platform. It is
// the address later layers key on — the query engine's inputs and symbols
// (P-2) and the module segment of belt: anchors (A-5). Identity is purely
// path-based: symbolic links are not resolved, so two links to the same file
// are two distinct FileIDs.
type FileID string

// fileID canonicalizes a root-relative path as written in the manifest.
func fileID(rel string) FileID {
	return FileID(path.Clean(filepath.ToSlash(rel)))
}

// File is one source file of the project: its identity, its absolute location
// on disk, and its raw content.
type File struct {
	ID   FileID
	Path string
	Data []byte
}

// Project is an opened masterbelt project.
type Project struct {
	// Root is the absolute path of the directory holding masterbelt.toml.
	Root string
	// Config is the parsed manifest.
	Config config.Config
	// Profile is the name of the profile the project was opened with; ""
	// is the default profile (the manifest's top-level keys).
	Profile string
	// Entry is the id of the entry point the opened profile names.
	Entry FileID

	files map[FileID]*File
}

// File returns the project file with the given id, or nil if the id is not
// part of the project.
func (p *Project) File(id FileID) *File { return p.files[id] }

// EntryFile returns the entry point's file.
func (p *Project) EntryFile() *File { return p.files[p.Entry] }

// Files returns the project's files ordered by id. In P-1 this is always the
// entry file alone; P-2 grows the set by following `use`.
func (p *Project) Files() []*File {
	out := make([]*File, 0, len(p.files))
	for _, f := range p.files {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// FindRoot walks from dir up to the filesystem root looking for the directory
// that holds masterbelt.toml, go.mod style. It reports the first hit as the
// project root.
func FindRoot(dir string) (string, bool) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", false
	}
	for {
		if fi, err := os.Stat(filepath.Join(dir, config.FileName)); err == nil && fi.Mode().IsRegular() {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// Open opens the project at or above dir with its default profile.
func Open(dir string) (*Project, diagnostic.List) {
	return OpenProfile(dir, "")
}

// OpenProfile finds the project root at or above dir, parses its manifest,
// resolves the profile — "" for the default, otherwise a [profile.<name>]
// section — and loads its entry file. On failure it returns nil and the
// problems found; every diagnostic is about the manifest (masterbelt.toml),
// never about masterbelt source — analyzing the loaded files is the caller's
// business.
func OpenProfile(dir, profile string) (*Project, diagnostic.List) {
	var diags diagnostic.List

	root, ok := FindRoot(dir)
	if !ok {
		diags.Add(config.Missing())
		return nil, diags
	}

	cfg, diags := config.Load(filepath.Join(root, config.FileName))
	if diags.HasErrors() {
		return nil, diags
	}

	rawEntry, ok := profileEntry(cfg, profile, &diags)
	if !ok {
		return nil, diags
	}

	entry := fileID(rawEntry)
	entryPath := filepath.Join(root, filepath.FromSlash(string(entry)))
	data, err := os.ReadFile(entryPath)
	if err != nil {
		if profile == "" {
			diags.Add(config.EntryNotFound(rawEntry))
		} else {
			diags.Add(config.ProfileEntryNotFound(profile, rawEntry))
		}
		return nil, diags
	}

	return &Project{
		Root:    root,
		Config:  cfg,
		Profile: profile,
		Entry:   entry,
		files: map[FileID]*File{
			entry: {ID: entry, Path: entryPath, Data: data},
		},
	}, diags
}

// profileEntry resolves the entry point of the requested profile. Parse has
// already vetted every declared entry, so the failures left are asking for a
// profile the manifest does not declare, or for the default profile of a
// manifest that only declares named ones.
func profileEntry(cfg config.Config, profile string, diags *diagnostic.List) (string, bool) {
	if profile == "" {
		if cfg.Entry == "" {
			diags.Add(config.MissingEntry())
			return "", false
		}
		return cfg.Entry, true
	}
	p, ok := cfg.Profiles[profile]
	if !ok {
		diags.Add(config.UnknownProfile(profile))
		return "", false
	}
	return p.Entry, true
}
