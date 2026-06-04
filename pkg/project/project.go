// Package project models a masterbelt project: the root directory marked by
// masterbelt.toml, the parsed manifest, and the set of source files reachable
// from the entry point — the closure of the entry's use declarations.
//
// The project layer owns the meaning of a use path: relative to the importing
// file, confined to the project root. It parses files (pkg/masterbelt/parser)
// to follow their imports, but never resolves names or types — the semantic
// layer consumes each File's resolved Uses table as an input, so the path
// semantics live in exactly one place. The dependency arrow keeps pointing
// downward: project reads the parsers, and semantic's callers (CLI, LSP) bind
// a Project to the engine; neither parser nor semantic imports project.
package project

import (
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/project/config"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
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
// on disk, its raw content, and its parsed syntax.
type File struct {
	ID   FileID
	Path string
	Data []byte
	// AST is the file's parsed, incrementally editable syntax document.
	AST *abstract.Document
	// Uses maps each of the file's use declarations to the FileID its path
	// resolves to. A use whose path is malformed, escapes the project root,
	// or names a file that cannot be read is absent — the file set simply
	// does not grow there, and the semantic layer reports it at the use site.
	Uses map[*ast.UseDecl]FileID
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
	entryFile, err := loadFile(root, entry)
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
		files:   closeOver(root, entryFile),
	}, diags
}

// loadFile reads and parses one project file.
func loadFile(root string, id FileID) (*File, error) {
	p := filepath.Join(root, filepath.FromSlash(string(id)))
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	return &File{ID: id, Path: p, Data: data, AST: abstract.NewDocument(data)}, nil
}

// closeOver follows use declarations from the entry until the file set is
// closed: every reachable file is loaded and parsed exactly once (a visited
// set makes import cycles terminate; reporting them is the semantic layer's
// job), and each file's Uses table records where its imports resolved.
func closeOver(root string, entry *File) map[FileID]*File {
	files := map[FileID]*File{entry.ID: entry}
	queue := []*File{entry}
	for len(queue) > 0 {
		f := queue[0]
		queue = queue[1:]

		f.Uses = map[*ast.UseDecl]FileID{}
		for _, u := range f.AST.File().Uses {
			target, ok := resolveUse(f.ID, u.Path)
			if !ok {
				continue // malformed or escaping: absent from the table
			}
			if _, seen := files[target]; !seen {
				loaded, err := loadFile(root, target)
				if err != nil {
					continue // no such file: absent from the table
				}
				files[target] = loaded
				queue = append(queue, loaded)
			}
			f.Uses[u] = target
		}
	}
	return files
}

// resolveUse resolves a use path as written in importer's source to the
// FileID it names: use paths are relative to the importing file and must stay
// inside the project root.
func resolveUse(importer FileID, usePath string) (FileID, bool) {
	if usePath == "" || path.IsAbs(usePath) {
		return "", false
	}
	target := path.Join(path.Dir(string(importer)), usePath) // Join also cleans
	if target == ".." || strings.HasPrefix(target, "../") {
		return "", false
	}
	return FileID(target), true
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
