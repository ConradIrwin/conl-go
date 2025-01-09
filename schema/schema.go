// package schema provides a mechanism to approximately validate
// a CONL document.
//
// A schema is a CONL document that defines the allowable structure
// of another CONL document.
package schema

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/ConradIrwin/conl-go"
)

// A Schema is a map of named definitions. The special name "root"
// is used to validate the root of the document.
//
// The values in the map are always themselves maps, with any of the following keys:
//
//   - "scalar": a matcher for a single scalar
//   - "one of": a list of matchers that are ORed together.
//   - "keys", "required keys": a map of keys and values (some of which are required).
//     Both the keys and values are matchers.
//   - "items", "required items": a list (with some number of required items)
//
// A matcher is a string with two possible formats:
//   - A reference to another definition in the schema, e.g. "<name>"
//   - A regular expression, e.g. [a-z]+. Regular expressions are anchored by default,
//     meaning they must match the entire string. If you want a substring match start and end with .*.
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
	return s.schema["root"].validate(s, doc, "")
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

func (d *definition) validate(s *Schema, val *conlValue, key string) (errors []ValidationError) {
	if val.Error != nil {
		errors = append(errors,
			ValidationError{
				lno: val.Lno,
				key: key,
				err: *val.Error,
			})
		return errors
	}

	if d.Scalar != nil {
		if val.Map != nil || val.List != nil {
			errors = append(errors,
				ValidationError{
					lno:           val.Lno,
					key:           key,
					expectedMatch: []string{"any scalar"},
				})
			return errors
		}
		return d.Scalar.validate(s, val, key)
	}

	if d.OneOf != nil {
		for _, item := range d.OneOf {
			nextErrors := item.validate(s, val, key)
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
					lno:           val.Lno,
					key:           key,
					expectedMatch: []string{"a map"},
				})
			return errors
		}

		for _, entry := range val.Map {
			allowed := false
			for keyMatcher, valueMatcher := range d.RequiredKeys {
				keyErrors := keyMatcher.validate(s, &conlValue{Lno: entry.Lno, Scalar: &entry.Key}, "")
				if len(keyErrors) == 0 {
					seenRequired[keyMatcher] = true
					allowed = true
					errors = append(errors, valueMatcher.validate(s, &entry.Value, entry.Key)...)
				}
			}
			if !allowed {
				for keyMatcher, valueMatcher := range d.Keys {
					keyErrors := keyMatcher.validate(s, &conlValue{Lno: entry.Lno, Scalar: &entry.Key}, "")
					if len(keyErrors) == 0 {
						allowed = true
						errors = append(errors, valueMatcher.validate(s, &entry.Value, entry.Key)...)
						break
					}
				}
			}
			if !allowed {
				errors = append(errors, ValidationError{
					lno:        entry.Lno,
					key:        key,
					unexpected: fmt.Sprintf("key %s", entry.Key),
				})
			}
		}

		requiredErrors := []ValidationError{}

		for keyMatcher := range d.RequiredKeys {
			if !seenRequired[keyMatcher] {
				errors = append(errors, ValidationError{
					lno:         val.Lno,
					key:         key,
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
					lno:           val.Lno,
					key:           key,
					expectedMatch: []string{"a list"},
				})
			return errors
		}
		for i, valueMatcher := range d.RequiredItems {
			if i < len(val.List) {
				errors = append(errors, valueMatcher.validate(s, &val.List[i].Value, "")...)
			}
		}
		if len(d.RequiredItems) > len(val.List) {
			errors = append(errors, ValidationError{
				lno:          val.Lno,
				key:          key,
				requiredItem: d.RequiredItems[len(val.List)].String(),
			})
		}
		if d.Items == nil && len(val.List) > len(d.RequiredItems) {
			errors = append(errors, ValidationError{
				lno:        val.List[len(d.RequiredItems)].Lno,
				key:        key,
				unexpected: "list item",
			})
		}
		if d.Items != nil {
			for i := len(d.RequiredItems); i < len(val.List); i++ {
				errors = append(errors, d.Items.validate(s, &val.List[i].Value, "")...)
			}
		}
		return errors
	}

	if val.List != nil || val.Map != nil || val.Scalar != nil {
		errors = append(errors,
			ValidationError{
				lno:           val.Lno,
				key:           key,
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

func (m *matcher) validate(s *Schema, val *conlValue, key string) (errors []ValidationError) {
	if m.Resolved != nil {
		return m.Resolved.validate(s, val, key)
	}
	if val.Scalar == nil {
		errors = append(errors,
			ValidationError{
				lno:           val.Lno,
				expectedMatch: []string{"any scalar"},
				key:           key,
			})
		return errors
	}
	if !m.Pattern.MatchString(*val.Scalar) {
		errors = append(errors, ValidationError{
			lno:           val.Lno,
			key:           key,
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
	err           string
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
	case ve.err != "":
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

type mapEntry struct {
	Lno   int
	Key   string
	Value conlValue
	Error *string
}

type listEntry struct {
	Lno   int
	Value conlValue
	Error *string
}

type conlValue struct {
	Lno    int
	Scalar *string
	Map    []mapEntry
	List   []listEntry
	Error  *string
}

func parseDoc(input []byte) *conlValue {
	root := &conlValue{Lno: 1}
	stack := []*conlValue{root}

	for lno, token := range conl.Tokens(input) {
		current := stack[len(stack)-1]
		value := token.Content

		switch token.Kind {
		case conl.MapKey:
			current.Map = append(current.Map, mapEntry{Lno: lno, Key: token.Content})

		case conl.ListItem:
			current.List = append(current.List, listEntry{Lno: lno})

		case conl.Value, conl.MultilineValue:
			if len(current.Map) > 0 {
				current.Map[len(current.Map)-1].Value = conlValue{Lno: lno, Scalar: &value}
			} else {
				current.List[len(current.List)-1].Value = conlValue{Lno: lno, Scalar: &value}
			}

		case conl.NoValue:
			if len(current.Map) > 0 {
				current.Map[len(current.Map)-1].Value = conlValue{Lno: current.Map[len(current.Map)-1].Lno}
			} else {
				current.List[len(current.List)-1].Value = conlValue{Lno: current.List[len(current.List)-1].Lno}
			}

		case conl.Indent:
			if len(current.Map) > 0 {
				current.Map[len(current.Map)-1].Value = conlValue{Lno: current.Map[len(current.Map)-1].Lno}
				stack = append(stack, &current.Map[len(current.Map)-1].Value)
			} else {
				current.List[len(current.List)-1].Value = conlValue{Lno: current.List[len(current.List)-1].Lno}
				stack = append(stack, &current.List[len(current.List)-1].Value)
			}
		case conl.Outdent:
			stack = stack[:len(stack)-1]
			takeError()

		case conl.Error:
			if len(current.Map) > 0 {
				current.Map = append(current.Map, mapEntry{Lno: lno, Error: &value})
			} else if len(current.List) > 0 {
				current.List = append(current.List, listEntry{Lno: lno, Error: &value})
			} else {
				current.Error = &value
			}

		case conl.MultilineHint, conl.Comment:
			takeError()
		default:
			panic(fmt.Errorf("%v: missing case %#v", lno, token))
		}
	}

	return root
}
