package ir

// Type is a masterbelt type. Integer literals are untyped constants (UntypedInt)
// whose default type is Int64; an annotation gives a constant a concrete type.
//
// This file holds only the type as data — the enum and its name. The rules that
// reason about types (range checks, lookup by name, the operator-method type
// rules, inference) live in package types, which depends on ir rather than the
// other way around.
type Type int

const (
	Invalid    Type = iota // could not be determined (unknown type name, cycle, ...)
	UntypedInt             // an un-annotated integer constant; defaults to Int64
	Int8
	Int16
	Int32
	Int64
	Uint8
	Uint16
	Uint32
	Uint64
	UntypedBool // an un-annotated boolean constant; defaults to Bool
	Bool
)

var typeNames = map[Type]string{
	Invalid:     "invalid",
	UntypedInt:  "untyped int",
	Int8:        "int8",
	Int16:       "int16",
	Int32:       "int32",
	Int64:       "int64",
	Uint8:       "uint8",
	Uint16:      "uint16",
	Uint32:      "uint32",
	Uint64:      "uint64",
	UntypedBool: "untyped bool",
	Bool:        "bool",
}

// String returns the type's name.
func (t Type) String() string {
	if name, ok := typeNames[t]; ok {
		return name
	}
	return "Type(?)"
}
