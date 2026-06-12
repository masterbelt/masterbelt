package master

// Registry resolves a source declaration's format name to the Format that reads
// it. The master core holds only the mapping; the integration layer above it
// registers the concrete formats (csv, later xlsx and sqlite), since those live
// outside the core — keeping the core free of any one format's dependency, the
// one-way import boundary the layer rests on.
type Registry struct {
	formats map[string]Format
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{formats: map[string]Format{}}
}

// Register adds f under its own Name. A second registration under a name already
// taken replaces the first; the integration layer owns the set it installs, so
// the last word is deliberately the caller's, not an error to recover from here.
func (r *Registry) Register(f Format) {
	r.formats[f.Name()] = f
}

// Lookup returns the format registered under name, or false when none is — the
// signal a source declaration named a format the toolchain does not know.
func (r *Registry) Lookup(name string) (Format, bool) {
	f, ok := r.formats[name]
	return f, ok
}
