// Package source models the text a compiler reads. It provides two byte
// buffers that share one Buffer interface — the immutable File and the
// editable Text — together with the position machinery (Position, Span,
// Encoding) that maps byte offsets to the line and column coordinates used by
// diagnostics and editor protocols.
package source
