// Package conl implements [CONL]  parsing and serializing.
//
// CONL is a post-modern, human-centric configuration language. It is designed
// to be a easy to read, easy to edit, and easy to parse, in that order.
// It uses a JSON-like structure of scalars, maps, and lists; an INI-like type system
// to defer scalar parsing until runtime; and a (simplified) YAML-like syntax for indentation.
//
//	; a basic CONL document
//	map
//	  a = b
//	list
//	  = a
//	  = b
//	scalar = value
//
// Like the builtin json package, CONL can automatically convert between Go types and CONL values.
//
// For example, you could parse the above document into a struct defined in Go as:
//
//	type Example struct {
//	  Map map[string]string `conl:"map"`
//	  List []string `conl:"list"`
//	  Scalar string `conl:"scalar"`
//	}
//
//	example := Example{}
//	conl.Unmarshal(data, &example)
//
// If your type implements the [encoding.TextMarshaler] and [encoding.TextUnmarshaler] then CONL
// will use that to convert between a scalar and your type, otherwise scalars are parsed using
// the [strconv] package.
//
// Package conl supports a very similar set of Go types to [encoding/json]. In particular, any
// string, number, or boolean value can be serialized; as can any struct, map, array, or slive
// of such values. On the flip side, channels and functions cannot be serialized. Unlike json,
// conl allows map keys to be numbers, bools, or arrays or structs of those types in addition to
// strings.
//
// [CONL]: https://conl.dev
package conl
