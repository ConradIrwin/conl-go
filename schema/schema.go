// package schema provides a mechanism to validate the structure
// of a CONL document.
//
// A schema is itself a CONL document that maps keys to definitions.
// The "root" is matched against the target document, and the other
// definitions can be used to build (potentially recursive) structures.
//
// A definition is a map with the following possible keys:
//   - "scalar" - the value must be a matching scalar.
//   - "keys" - a map of matchers to matchers. The target document
//     must contain a map at this position. For each key value pair in the
//     target document, the key must match one of the matchers in the map,
//     and the value must match its corresponding value (unless they were already
//     matched by "required keys")
//   - "required keys" - a map of matchers to matchers. The target document
//     must contain a map at this position, and at least one key value pair
//     in the map must match a key value pair in the required keys map.
//   - "items" - a single matchers. The target document
//     must contain a list at this position. Each item in the list (that
//     is not matched by a "required items" in the same definition) must
//     match.
//   - "required items" - a list of matchers. The target document
//     must contain a list at this position. Each item in the list in the target document
//     must match the corresponding matcher in the definition. No extra items are allowed
//     unless "items" is also specified.
//   - "one of" - a list of matchers. The target document must match one of them.
//
// Other than "keys" and "required keys", or "items" and "required items",
// which can be paired; the definition must only have one key.
//
// The matchers are scalars that either define a regular expression to match against
// a scalar int he document; or reference another definition in the schema. If the matcher
// is of the form <.*> it refers to an existing definition; otherwise it is a regular expression
// that matches a scalar. The regular expressions must match the entire value, so (for example):
// "a" matches "a", but not "cat".
//
// # Examples
//
// This example schema
//
//	root
//	  required keys
//	    version = \d+
//	  keys
//	    id = [a-zA-Z]+
//
// matches the CONL documents
//
//	version = 1
//
// or,
//
//	version = 1
//	id = elephant
//
// but not
//
//	id = elephant ; missing required key "version"
//
// or
//
//	version = 1
//	id = elephant
//	name = "The Elephant" ; error unexpected key
//
// This example schema:
//
//	root
//	  keys
//	    authors = <author>
//	author
//	  one of
//	    = <author details>
//	    = <author list>
//	author details
//	  scalar = .+ <.+@.+>
//	author list
//	  items = <author details>
//
// Matches
//
//	authors = Conrad <conrad.irwin@gmail.com>
//
// or
//
//	authors
//	  = Conrad <conrad.irwin@gmail.com>
//	  = Kate <kate@example.com>
//
// but not
//
//	authors = conrad.irwin@gmail.com ; error expected authors to match .+ <.+@.+>
//
// or
//
//	authors
//	  = Kate
package schema

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/ConradIrwin/conl-go"
)

// A Schema allows you to validate a CONL document against a set of rules.
type Schema struct {
	schema map[string]*definition
}

// Parse a schema from the given input.
// An error is returned if the input is not valid CONL,
// or if the schema contains references to definitions that don't exist,
// invalid regular expressions, or circular references.
func Parse(input []byte) (*Schema, error) {
	s := &Schema{schema: map[string]*definition{}}
	if err := conl.Unmarshal(input, &s.schema); err != nil {
		return nil, err
	}
	if _, ok := s.schema["root"]; !ok {
		return nil, fmt.Errorf("invalid schema: missing \"root\"")
	}
	for k, v := range s.schema {
		if err := v.resolve(s, k, []string{}); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// Validate validates the input against the schema.
// If the input is valid CONL and matches the schema, nil is returned.
// Otherwise, it returns a non-empty slice of errors (including any errors
// that would have been reported by conl.Parse).
// As there may be multiple possible ways for a schema to match,
// the errors returned are an arbitrary subset of the possible problems.
// The exact errors returned will change over time as heuristics improve.
func (s *Schema) Validate(input []byte) []ValidationError {
	doc := parseDoc(input)
	return s.schema["root"].validate(s, doc, &conl.Token{Lno: 1})
}

type definition struct {
	Name string `conl:"-"`
	Docs string `conl:"docs"`

	Scalar *matcher `conl:"scalar"`

	OneOf []*matcher `conl:"one of"`

	Keys         map[*matcher]*matcher `conl:"keys"`
	RequiredKeys map[*matcher]*matcher `conl:"required keys"`

	Items         *matcher   `conl:"items"`
	RequiredItems []*matcher `conl:"required items"`
}

func sumIf(bs ...bool) int {
	count := 0
	for _, b := range bs {
		if b {
			count += 1
		}
	}
	return count
}

func (d *definition) resolve(s *Schema, name string, seen []string) error {

	if d.Name != "" {
		return nil
	}
	count := sumIf(d.Scalar != nil,
		d.OneOf != nil,
		d.Keys != nil || d.RequiredKeys != nil,
		d.Items != nil || d.RequiredItems != nil)

	if count > 1 {
		return fmt.Errorf("invalid schema: %v must have only one of pattern, enum, (required) keys, or (required) items", name)
	}
	if d.Scalar != nil {
		if err := d.Scalar.resolve(s, seen); err != nil {
			return err
		}
	}
	for _, choice := range d.OneOf {
		if err := choice.resolve(s, seen); err != nil {
			return err
		}
	}
	for pat, key := range d.Keys {
		if err := pat.resolve(s, seen); err != nil {
			return err
		}
		if err := key.resolve(s, seen); err != nil {
			return err
		}
	}
	for pat, key := range d.RequiredKeys {
		if err := pat.resolve(s, seen); err != nil {
			return err
		}
		if err := key.resolve(s, seen); err != nil {
			return err
		}
	}
	if d.Items != nil {
		if err := d.Items.resolve(s, seen); err != nil {
			return err
		}
	}
	for _, item := range d.RequiredItems {
		if err := item.resolve(s, seen); err != nil {
			return err
		}
	}
	return nil
}

func (d *definition) validate(s *Schema, val *conlValue, pos *conl.Token) (errors []ValidationError) {
	if val.Scalar != nil && val.Scalar.Error != nil {
		errors = append(errors,
			ValidationError{
				lno: pos.Lno,
				key: pos.Content,
				err: val.Scalar.Error,
			})
		return errors
	}

	if d.Scalar != nil {
		if val.Map != nil || val.List != nil {
			errors = append(errors,
				ValidationError{
					lno:           pos.Lno,
					key:           pos.Content,
					expectedMatch: []string{"any scalar"},
				})
			return errors
		}
		return d.Scalar.validate(s, val, pos)
	}

	if d.OneOf != nil {
		for _, item := range d.OneOf {
			nextErrors := item.validate(s, val, pos)
			if len(nextErrors) == 0 {
				return nil
			}
			if len(errors) == 0 || len(nextErrors) < len(errors) || nextErrors[0].lno >= errors[0].lno {
				errors = mergeErrors(nextErrors, errors)
			} else {
				errors = mergeErrors(errors, nextErrors)
			}
		}
		return errors
	}

	if d.Keys != nil || d.RequiredKeys != nil {
		seenRequired := make(map[*matcher]bool)
		if val.Scalar != nil || val.List != nil {
			errors = append(errors,
				ValidationError{
					lno:           pos.Lno,
					key:           pos.Content,
					expectedMatch: []string{"a map"},
				})
			return errors
		}

		for _, entry := range val.Map {
			allowed := false
			if entry.Key.Error != nil {
				errors = append(errors, ValidationError{
					lno: entry.Key.Lno,
					err: entry.Key.Error,
				})
				continue
			}
			for keyMatcher, valueMatcher := range d.RequiredKeys {
				keyErrors := keyMatcher.validate(s, &conlValue{Scalar: entry.Key}, &conl.Token{Lno: entry.Key.Lno})
				if len(keyErrors) == 0 {
					seenRequired[keyMatcher] = true
					allowed = true
					errors = append(errors, valueMatcher.validate(s, &entry.Value, entry.Key)...)
				}
			}
			if !allowed {
				for keyMatcher, valueMatcher := range d.Keys {
					keyErrors := keyMatcher.validate(s, &conlValue{Scalar: entry.Key}, &conl.Token{Lno: entry.Key.Lno})
					if len(keyErrors) == 0 {
						allowed = true
						errors = append(errors, valueMatcher.validate(s, &entry.Value, entry.Key)...)
						break
					}
				}
			}
			if !allowed {
				errors = append(errors, ValidationError{
					lno:        entry.Key.Lno,
					key:        entry.Key.Content,
					unexpected: fmt.Sprintf("key %s", entry.Key.Content),
				})
			}
		}

		requiredErrors := []ValidationError{}

		for keyMatcher := range d.RequiredKeys {
			if !seenRequired[keyMatcher] {
				errors = append(errors, ValidationError{
					lno:         pos.Lno,
					key:         pos.Content,
					requiredKey: []string{keyMatcher.String()},
				})
			}
		}
		if len(requiredErrors) > 0 {
			return requiredErrors
		}
		return errors
	}

	if d.Items != nil || d.RequiredItems != nil {
		if val.Scalar != nil || val.Map != nil {
			errors = append(errors,
				ValidationError{
					lno:           pos.Lno,
					key:           pos.Content,
					expectedMatch: []string{"a list"},
				})
			return errors
		}
		for i, valueMatcher := range d.RequiredItems {
			if i < len(val.List) {
				entry := &val.List[i]

				if entry.Key.Error != nil {
					errors = append(errors, ValidationError{
						lno: entry.Key.Lno,
						err: entry.Key.Error,
					})
					continue
				}
				errors = append(errors, valueMatcher.validate(s, &entry.Value, entry.Key)...)
			}
		}
		if len(d.RequiredItems) > len(val.List) {
			errors = append(errors, ValidationError{
				lno:          pos.Lno,
				key:          pos.Content,
				requiredItem: d.RequiredItems[len(val.List)].String(),
			})
		}
		if d.Items == nil && len(val.List) > len(d.RequiredItems) {
			errors = append(errors, ValidationError{
				lno:        val.List[len(d.RequiredItems)].Key.Lno,
				key:        pos.Content,
				unexpected: "list item",
			})
		}
		for i := len(d.RequiredItems); i < len(val.List); i++ {
			entry := &val.List[i]

			if entry.Key.Error != nil {
				errors = append(errors, ValidationError{
					lno: entry.Key.Lno,
					err: entry.Key.Error,
				})
				continue
			}
			if d.Items != nil {
				errors = append(errors, d.Items.validate(s, &entry.Value, entry.Key)...)
			}
		}
		return errors
	}

	if val.List != nil || val.Map != nil || val.Scalar != nil {
		errors = append(errors,
			ValidationError{
				lno:           pos.Lno,
				key:           pos.Content,
				expectedMatch: []string{"no value"},
			})
	}
	return errors

}

type matcher struct {
	Pattern   *regexp.Regexp
	Reference string
	Resolved  *definition
}

func (m *matcher) resolve(s *Schema, seen []string) error {
	if m.Pattern != nil || m.Resolved != nil {
		return nil
	}
	next, ok := s.schema[m.Reference]
	if !ok {
		return fmt.Errorf("<%s> is not defined", m.Reference)
	} else if slices.Contains(seen, m.Reference) {
		return fmt.Errorf("<%s> is defined in terms of itself", m.Reference)
	}
	if err := next.resolve(s, m.Reference, append(seen, m.Reference)); err != nil {
		return err
	}
	m.Resolved = next
	return nil
}

func (m *matcher) validate(s *Schema, val *conlValue, pos *conl.Token) (errors []ValidationError) {
	if m.Resolved != nil {
		return m.Resolved.validate(s, val, pos)
	}
	if val.Scalar == nil {
		errors = append(errors,
			ValidationError{
				lno:           pos.Lno,
				expectedMatch: []string{"any scalar"},
				key:           pos.Content,
			})
		return errors
	}
	if val.Scalar.Error != nil {
		errors = append(errors,
			ValidationError{
				lno: pos.Lno,
				err: val.Scalar.Error,
				key: pos.Content,
			})
		return errors
	}
	if !m.Pattern.MatchString(val.Scalar.Content) {
		errors = append(errors, ValidationError{
			lno:           pos.Lno,
			key:           pos.Content,
			expectedMatch: []string{m.String()},
		})
		return errors
	}
	return nil
}

func (m *matcher) UnmarshalText(data []byte) error {
	if data[0] == '<' {
		if data[len(data)-1] != '>' {
			return fmt.Errorf("missing closing >")
		}
		m.Reference = string(data[1 : len(data)-1])
		return nil
	}
	pattern := &regexp.Regexp{}
	if err := pattern.UnmarshalText([]byte("^" + string(data) + "$")); err != nil {
		return err
	}
	m.Pattern = pattern
	return nil
}

func (m *matcher) String() string {
	if m.Pattern != nil {
		s := m.Pattern.String()
		s = s[1 : len(s)-1]
		if s[0] == '<' {
			s = "\\" + s
		}
		return s
	}
	return "<" + m.Reference + ">"
}

func (m *matcher) MarshalText() ([]byte, error) {
	return []byte(m.String()), nil
}

// A ValidationError represents a single validation error.
// Use .Error() to get the message, and use .Lno to get the line number.
type ValidationError struct {
	key           string
	expectedMatch []string
	requiredKey   []string
	requiredItem  string
	unexpected    string
	err           error
	lno           int
}

func joinWithOr(items []string) string {
	if len(items) == 0 {
		return ""
	}
	if len(items) == 1 {
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " or " + items[len(items)-1]
}

// Lno returns the 1-indexed line number on which the error occurred.
func (ve *ValidationError) Lno() int {
	return ve.lno
}

func (ve *ValidationError) Error() string {
	switch true {
	case ve.err != nil:
		return fmt.Sprintf("%d: %v", ve.lno, ve.err)

	case ve.requiredKey != nil:
		return fmt.Sprintf("%d: missing required key %v", ve.lno, joinWithOr(ve.requiredKey))

	case ve.requiredItem != "":
		return fmt.Sprintf("%d: missing required list item %v", ve.lno, ve.requiredItem)

	case ve.expectedMatch != nil:
		if ve.key != "" {
			return fmt.Sprintf("%d: expected %s = %v", ve.lno, ve.key, joinWithOr(ve.expectedMatch))
		} else {
			return fmt.Sprintf("%d: expected %v", ve.lno, joinWithOr(ve.expectedMatch))
		}

	case ve.unexpected != "":
		return fmt.Sprintf("%d: unexpected %v", ve.lno, ve.unexpected)

	default:
		panic(fmt.Errorf("unhandled %#v", ve))
	}
}

func mergeErrors(a, b []ValidationError) []ValidationError {
	merged := make([]ValidationError, 0)
	aMap := make(map[int]ValidationError)

	for _, err := range a {
		aMap[err.lno] = err
	}

	for _, errB := range b {
		if errA, exists := aMap[errB.lno]; exists {
			merged = append(merged, ValidationError{
				key:           errA.key,
				expectedMatch: append(errB.expectedMatch, errA.expectedMatch...),
				requiredKey:   append(errB.requiredKey, errA.requiredKey...),
				requiredItem:  errA.requiredItem,
				unexpected:    errA.unexpected,
				err:           errA.err,
				lno:           errA.lno,
			})
			delete(aMap, errB.lno)
		}
	}

	for _, errA := range aMap {
		merged = append(merged, errA)
	}

	slices.SortFunc(merged, func(i, j ValidationError) int {
		return i.lno - j.lno
	})

	return merged
}

type entry struct {
	Key   *conl.Token
	Value conlValue
}

type conlValue struct {
	Scalar *conl.Token
	Map    []entry
	List   []entry
}

func parseDoc(input []byte) *conlValue {
	root := &conlValue{}
	stack := []*conlValue{root}

	for token := range conl.Tokens(input) {
		current := stack[len(stack)-1]

		switch token.Kind {
		case conl.MapKey:
			current.Map = append(current.Map, entry{Key: &token})

		case conl.ListItem:
			current.List = append(current.List, entry{Key: &token})

		case conl.Scalar, conl.MultilineScalar:
			if len(current.Map) > 0 {
				current.Map[len(current.Map)-1].Value = conlValue{Scalar: &token}
			} else {
				current.List[len(current.List)-1].Value = conlValue{Scalar: &token}
			}

		case conl.Indent:
			if len(current.Map) > 0 {
				current.Map[len(current.Map)-1].Value = conlValue{}
				stack = append(stack, &current.Map[len(current.Map)-1].Value)
			} else {
				current.List[len(current.List)-1].Value = conlValue{}
				stack = append(stack, &current.List[len(current.List)-1].Value)
			}
		case conl.Outdent:
			stack = stack[:len(stack)-1]

		case conl.NoValue, conl.MultilineHint, conl.Comment:
		default:
			panic(fmt.Errorf("%v: missing case %#v", token.Lno, token))
		}
	}

	return root
}
