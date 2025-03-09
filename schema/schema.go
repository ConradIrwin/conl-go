// package schema provides a mechanism to validate the structure
// of a CONL document.
package schema

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/ConradIrwin/conl-go"
	"github.com/ConradIrwin/dbg"
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
func (s *Schema) Validate(input []byte) *Result {
	doc := parseDoc(input)
	rootToken := &conl.Token{Lno: 1}
	guesses, errors := s.schema["root"].validate(s, doc, rootToken)
	slices.SortFunc(errors, func(i, j ValidationError) int {
		return i.token.Lno - j.token.Lno
	})
	return &Result{
		errors,
		doc,
		rootToken,
		guesses,
		s,
	}
}

type Result struct {
	errors    []ValidationError
	doc       *conlValue
	rootToken *conl.Token
	guesses   map[*conl.Token]*matcher
	schema    *Schema
}

func (r *Result) Valid() bool {
	return len(r.errors) == 0
}

func (r *Result) Errors() []ValidationError {
	return r.errors
}

func (r *Result) matcherForLine(lno int) *matcher {
	value := r.doc
outer:
	for {
		entries := value.Map
		if len(value.List) > 0 {
			entries = value.List
		}
		for i, entry := range entries {
			if entry.key.Lno == lno {
				return r.guesses[entry.key]
			} else if i == len(entries)-1 || entries[i+1].key.Lno > lno {
				value = &entry.value
				continue outer
			}
		}
		return nil
	}
}

// SuggestedKeys returns possible keys for the map defined on line `lno`
// (or for the root of the document if lno == 0)
// If `lno` defines a list, []string{"="}
func (r *Result) SuggestedKeys(lno int) []string {
	definition := r.schema.schema["root"]
	if lno > 0 {
		matcher := r.matcherForLine(lno)
		if matcher == nil || matcher.Resolved == nil {
			return nil
		}
		definition = matcher.Resolved
	}
	possible, listAllowed := definition.suggestedKeys()

	for i, m := range possible {
		for _, entry := range r.doc.Map {
			_, keyErrors := m.validate(r.schema, &conlValue{Scalar: entry.key}, entry.key)
			if len(keyErrors) == 0 {
				possible[i] = nil
				break
			}
		}
	}
	var results []string
	for _, m := range possible {
		if m == nil {
			continue
		}
		results = append(results, m.String())
	}
	if listAllowed {
		results = append(results, "=")
	}
	slices.Sort(results)
	return results
}

// SuggestedValues returns possible values for the key on line `lno`
// (or for the root of the document if lno == 0)
// If the value may be a list or an object, "\n  " is included in the response.
func (r *Result) SuggestedValues(lno int) []string {
	dbg.Dbg(r.guesses[r.rootToken])
	definition := r.schema.schema["root"]
	if lno > 0 {
		matcher := r.matcherForLine(lno)
		if matcher == nil {
			return nil
		}
		if matcher.Resolved == nil {
			return []string{matcher.String()}
		}

		definition = matcher.Resolved
	}
	p, indentAllowed := definition.suggestedValues()
	if indentAllowed {
		p = append(p, "\n  ")
	}
	return p
}

var anySchema *Schema
var once sync.Once

// Any is a schema that validates any CONL document.
func Any() *Schema {
	once.Do(func() {
		sch, err := Parse([]byte(`
root
  one of
    = <map>
    = <list>
    = .*
list
  items = <root>
map
  keys
    .* = <root>
`))
		if err != nil {
			panic(err)
		}
		anySchema = sch
	})
	return anySchema
}

// Validate a CONL document. The `load()` function will be called once. If a top-level "schema"
// key is present, it's value is passed, otherwise "" is given. If the load function is nil,
// or returns nil, then [Any] is used. If the load function returns an error it is returned
// as a ValidationError on either the token providing the schema definition, or the first token
// in the file, in addition to any errors that would be reported by conl.Parse.
func Validate(input []byte, load func(schema string) (*Schema, error)) *Result {
	doc := parseDoc(input)
	var schema *Schema
	var err error
	var validationError *ValidationError
	for _, entry := range doc.Map {
		if entry.key != nil && entry.key.Content == "schema" && entry.value.Scalar != nil {
			schema, err = load(entry.value.Scalar.Content)
			load = nil
			if err != nil {
				validationError = &ValidationError{
					err:   err,
					token: entry.value.Scalar,
				}
			}
			break
		}
	}
	if load != nil {
		schema, err = load("")
		if err != nil {
			for _, entry := range doc.Map {
				if entry.key != nil {
					validationError = &ValidationError{
						err:   err,
						token: entry.key,
					}
					break
				}
			}
		}
	}

	if schema == nil {
		schema = Any()
	}
	rootToken := &conl.Token{Lno: 1}

	guesses, errors := schema.schema["root"].validate(schema, doc, rootToken)
	if validationError != nil {
		errors = append(errors, *validationError)
	}
	slices.SortFunc(errors, func(i, j ValidationError) int {
		return i.token.Lno - j.token.Lno
	})
	return &Result{
		errors,
		doc,
		rootToken,
		guesses,
		schema,
	}
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
		if err := pat.resolve(s, nil); err != nil {
			return err
		}
		if err := key.resolve(s, nil); err != nil {
			return err
		}
	}
	for pat, key := range d.RequiredKeys {
		if err := pat.resolve(s, nil); err != nil {
			return err
		}
		if err := key.resolve(s, nil); err != nil {
			return err
		}
	}
	if d.Items != nil {
		if err := d.Items.resolve(s, nil); err != nil {
			return err
		}
	}
	for _, item := range d.RequiredItems {
		if err := item.resolve(s, nil); err != nil {
			return err
		}
	}
	return nil
}

func withItem[K comparable, V any](m map[K]V, key K, item V) map[K]V {
	if m == nil {
		m = map[K]V{}
	}
	m[key] = item
	return m
}

func (d *definition) validate(s *Schema, val *conlValue, pos *conl.Token) (map[*conl.Token]*matcher, []ValidationError) {
	if val.Scalar != nil && val.Scalar.Error != nil {
		return nil, []ValidationError{{
			token: val.Scalar,
			err:   val.Scalar.Error,
		}}
	}

	if d.Scalar != nil {
		if val.Map != nil || val.List != nil {
			token := pos
			if val.Scalar != nil {
				token = val.Scalar
			}
			return nil, []ValidationError{{
				token:         token,
				expectedMatch: []string{"any scalar"},
			}}
		}
		guesses, errors := d.Scalar.validate(s, val, pos)
		return withItem(guesses, pos, d.Scalar), errors
	}

	if d.OneOf != nil {
		var bestGuesses map[*conl.Token]*matcher
		var errors []ValidationError
		for _, item := range d.OneOf {
			guesses, nextErrors := item.validate(s, val, pos)
			if len(nextErrors) == 0 {
				return withItem(guesses, pos, item), nil
			}
			if len(errors) == 0 || nextErrors[0].Lno() >= errors[0].Lno() {
				bestGuesses = withItem(guesses, pos, item)
				errors = mergeErrors(nextErrors, errors)
			} else {
				errors = mergeErrors(errors, nextErrors)
			}
		}
		return bestGuesses, errors
	}

	if d.Keys != nil || d.RequiredKeys != nil {
		seenRequired := make(map[*matcher]bool)
		seenKeys := make(map[string]bool)

		if val.Scalar != nil || val.List != nil {
			token := pos
			if val.Scalar != nil {
				token = val.Scalar
			}
			return nil, []ValidationError{{
				token:         token,
				expectedMatch: []string{"a map"},
			}}
		}

		var errors []ValidationError
		guesses := map[*conl.Token]*matcher{}

		for _, entry := range val.Map {
			if entry.key.Error != nil {
				errors = append(errors, ValidationError{
					token: entry.key,
					err:   entry.key.Error,
				})
				continue
			}
			if seenKeys[entry.key.Content] {
				errors = append(errors, ValidationError{
					token:        entry.key,
					duplicateKey: entry.key.Content,
				})
				continue
			} else {
				seenKeys[entry.key.Content] = true
			}
			oneOf := []*matcher{}

			for keyMatcher, valueMatcher := range d.RequiredKeys {
				_, keyErrors := keyMatcher.validate(s, &conlValue{Scalar: entry.key}, entry.key)
				if len(keyErrors) == 0 {
					if seenRequired[keyMatcher] {
						errors = append(errors, ValidationError{
							token:        entry.key,
							duplicateKey: fmt.Sprintf("%s", keyMatcher),
						})
					} else {
						seenRequired[keyMatcher] = true
					}
					oneOf = append(oneOf, valueMatcher)
				}
			}
			for keyMatcher, valueMatcher := range d.Keys {
				_, keyErrors := keyMatcher.validate(s, &conlValue{Scalar: entry.key}, entry.key)
				if len(keyErrors) == 0 {
					oneOf = append(oneOf, valueMatcher)
				}
			}
			if len(oneOf) == 0 {
				errors = append(errors, ValidationError{
					token:      entry.key,
					unexpected: fmt.Sprintf("key %s", entry.key.Content),
				})
				continue
			}
			var itemErrors []ValidationError
			var bestGuesses map[*conl.Token]*matcher
			for _, item := range oneOf {
				guesses, nextErrors := item.validate(s, &entry.value, entry.key)
				if len(nextErrors) == 0 {
					bestGuesses = withItem(guesses, entry.key, item)
					itemErrors = nil
					break
				}
				if len(itemErrors) == 0 || nextErrors[0].Lno() >= itemErrors[0].Lno() {
					bestGuesses = withItem(guesses, entry.key, item)
					itemErrors = mergeErrors(nextErrors, itemErrors)
				} else {
					itemErrors = mergeErrors(itemErrors, nextErrors)
				}
			}
			for k, v := range bestGuesses {
				guesses[k] = v
			}
			errors = append(errors, itemErrors...)
		}

		for keyMatcher := range d.RequiredKeys {
			if !seenRequired[keyMatcher] {
				errors = append(errors, ValidationError{
					token:       pos,
					requiredKey: []string{keyMatcher.String()},
				})
			}
		}
		return guesses, errors
	}

	if d.Items != nil || d.RequiredItems != nil {
		if val.Scalar != nil || val.Map != nil {
			token := pos
			if val.Scalar != nil {
				token = val.Scalar
			}
			return nil, []ValidationError{{
				token:         token,
				expectedMatch: []string{"a list"},
			}}
		}

		var errors []ValidationError
		guesses := map[*conl.Token]*matcher{}

		for i, valueMatcher := range d.RequiredItems {
			if i < len(val.List) {
				entry := &val.List[i]

				if entry.key.Error != nil {
					errors = append(errors, ValidationError{
						token: entry.key,
						err:   entry.key.Error,
					})
					continue
				}
				more, errs := valueMatcher.validate(s, &entry.value, entry.key)
				guesses[entry.key] = valueMatcher
				for k, v := range more {
					guesses[k] = v
				}
				errors = append(errors, errs...)
			}
		}
		if len(d.RequiredItems) > len(val.List) {
			errors = append(errors, ValidationError{
				token:        pos,
				requiredItem: d.RequiredItems[len(val.List)].String(),
			})
		}
		if d.Items == nil && len(val.List) > len(d.RequiredItems) {
			errors = append(errors, ValidationError{
				token:      val.List[len(d.RequiredItems)].key,
				unexpected: "list item",
			})
		}
		for i := len(d.RequiredItems); i < len(val.List); i++ {
			entry := &val.List[i]

			if entry.key.Error != nil {
				errors = append(errors, ValidationError{
					token: entry.key,
					err:   entry.key.Error,
				})
				continue
			}
			if d.Items != nil {
				more, errs := d.Items.validate(s, &entry.value, entry.key)
				guesses[entry.key] = d.Items
				for k, v := range more {
					guesses[k] = v
				}
				errors = append(errors, errs...)
			}
		}
		return guesses, errors
	}

	if val.List != nil || val.Map != nil || val.Scalar != nil {
		token := val.Scalar
		if token == nil {
			token = pos
		}
		return nil, []ValidationError{{
			token:         token,
			expectedMatch: []string{"no value"},
		}}
	}

	return nil, nil
}

func (d *definition) suggestedKeys() ([]*matcher, bool) {
	var possible []*matcher
	listAllowed := false
	for m := range d.RequiredKeys {
		possible = append(possible, m)
	}
	for m := range d.Keys {
		possible = append(possible, m)
	}
	for _, oneOf := range d.OneOf {
		if oneOf.Resolved != nil {
			more, allowed := oneOf.Resolved.suggestedKeys()
			possible = append(possible, more...)
			if allowed {
				listAllowed = true
			}
		}
	}
	if d.Items != nil || len(d.RequiredItems) > 0 {
		listAllowed = true
	}
	return possible, listAllowed
}

func (d *definition) suggestedValues() ([]string, bool) {
	var possible []string
	indentAllowed := false
	if len(d.RequiredKeys) > 0 || len(d.Keys) > 0 || len(d.RequiredItems) > 0 || d.Items != nil {
		indentAllowed = true
	}
	if d.Scalar != nil {
		p, i := d.Scalar.suggestedValues()
		possible = append(possible, p...)
		if i {
			indentAllowed = true
		}
	}

	for _, oneOf := range d.OneOf {
		p, i := oneOf.suggestedValues()
		possible = append(possible, p...)
		if i {
			indentAllowed = true
		}
	}
	return possible, indentAllowed
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
	m.Resolved = next
	if err := next.resolve(s, m.Reference, append(seen, m.Reference)); err != nil {
		return err
	}
	return nil
}

func (m *matcher) validate(s *Schema, val *conlValue, pos *conl.Token) (map[*conl.Token]*matcher, []ValidationError) {
	if m.Resolved != nil {
		return m.Resolved.validate(s, val, pos)
	}
	if val.Scalar == nil {
		return nil, []ValidationError{{
			token:         pos,
			expectedMatch: []string{"any scalar"},
		}}
	}
	if val.Scalar.Error != nil {
		return nil, []ValidationError{{
			token: val.Scalar,
			err:   val.Scalar.Error,
		}}
	}
	if !m.Pattern.MatchString(val.Scalar.Content) {
		return nil, []ValidationError{{
			token:         val.Scalar,
			expectedMatch: []string{m.String()},
		}}
	}
	return nil, nil
}

func (m *matcher) suggestedValues() ([]string, bool) {
	if m.Resolved != nil {
		return m.Resolved.suggestedValues()
	}
	return []string{m.String()}, false
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
	if err := pattern.UnmarshalText([]byte("(?s)^" + string(data) + "$")); err != nil {
		return err
	}
	m.Pattern = pattern
	return nil
}

func (m *matcher) String() string {
	if m.Pattern != nil {
		s := m.Pattern.String()
		s = s[5 : len(s)-1]
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
	expectedMatch []string
	requiredKey   []string
	duplicateKey  string
	requiredItem  string
	unexpected    string
	err           error
	token         *conl.Token
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
	return ve.token.Lno
}

var quotedLiteral = regexp.MustCompile(`^"(?:[^\\"]|\\.)*"`)

// returns the range for the key or list item, value, and comment
func splitLine(line string) (int, int, int, int, int) {
	trimmed := strings.TrimLeft(line, " \t")
	startKey := len(line) - len(trimmed)
	trimmed =
		quotedLiteral.ReplaceAllStringFunc(trimmed, func(quoted string) string {
			return strings.Repeat("a", len(quoted))
		})

	endKey := len(line)
	startValue := len(line)
	if strings.HasPrefix(trimmed, "=") {
		endKey = startKey + 1
		startValue = endKey
	} else if found := strings.IndexAny(trimmed, "=;"); found > -1 {
		endKey = startKey + len(strings.TrimRight(trimmed[:found], " \t"))
		if trimmed[found] == '=' {
			startValue = startKey + found + 1
		} else {
			startValue = startKey + found
		}
	} else {
		endKey = startKey + len(strings.TrimRight(trimmed, " \t"))
	}
	valueHalf := line[startValue:]
	trimmed = strings.TrimLeft(valueHalf, " \t")
	startValue += len(valueHalf) - len(trimmed)

	trimmed =
		quotedLiteral.ReplaceAllStringFunc(trimmed, func(quoted string) string {
			return strings.Repeat("a", len(quoted))
		})

	endValue := len(line)
	startComment := len(line)
	if found := strings.Index(trimmed, ";"); found > -1 {
		endValue = startValue + len(strings.TrimRight(trimmed[:found], " \t"))
		startComment = startValue + found
	} else {
		endValue = startValue + len(strings.TrimRight(trimmed, " \t"))
	}

	return startKey, endKey, startValue, endValue, startComment
}

// RuneRange returns the 0-based utf-8 based range at which the error
// occurred (assuming that the provided line corresponds to Lno in the
// original document).
func (ve *ValidationError) RuneRange(line string) (int, int) {
	switch ve.token.Kind {
	case conl.Indent:
		start, _, _, _, _ := splitLine(line)
		return 0, start
	case conl.ListItem, conl.MapKey:
		start, end, _, _, _ := splitLine(line)
		return start, end
	case conl.MultilineScalar, conl.Scalar:
		_, _, start, end, _ := splitLine(line)
		return start, end

	case conl.Comment:
		_, _, _, _, start := splitLine(line)
		return start, len(line)

	default:
		startKey, _, _, endValue, _ := splitLine(line)
		return startKey, endValue
	}
}

// Msg returns a human-readable description of the problem suitable for
// showing to end-users.
func (ve *ValidationError) Msg() string {
	switch true {
	case ve.err != nil:
		return ve.err.Error()

	case ve.requiredKey != nil:
		return fmt.Sprintf("missing required key %v", joinWithOr(ve.requiredKey))

	case ve.requiredItem != "":
		return fmt.Sprintf("missing required list item %v", ve.requiredItem)

	case ve.expectedMatch != nil:
		return fmt.Sprintf("expected %v", joinWithOr(ve.expectedMatch))

	case ve.unexpected != "":
		return fmt.Sprintf("unexpected %v", ve.unexpected)

	case ve.duplicateKey != "":
		return fmt.Sprintf("duplicate key %v", ve.duplicateKey)

	default:
		panic(fmt.Errorf("unhandled %#v", ve))
	}
}

// Error implements the error interface
func (ve *ValidationError) Error() string {
	return fmt.Sprintf("%d: %s", ve.Lno(), ve.Msg())
}

func mergeErrors(a, b []ValidationError) []ValidationError {
	merged := make([]ValidationError, 0)
	aMap := make(map[*conl.Token]ValidationError)

	for _, err := range a {
		aMap[err.token] = err
	}

	for _, errB := range b {
		if errA, exists := aMap[errB.token]; exists {
			expected := append(errB.expectedMatch, errA.expectedMatch...)
			slices.Sort(expected)
			required := append(errB.requiredKey, errA.requiredKey...)
			slices.Sort(required)
			merged = append(merged, ValidationError{
				expectedMatch: slices.Compact(expected),
				requiredKey:   slices.Compact(required),
				requiredItem:  errA.requiredItem,
				unexpected:    errA.unexpected,
				err:           errA.err,
				token:         errA.token,
			})
			delete(aMap, errB.token)
		}
	}

	for _, errA := range aMap {
		merged = append(merged, errA)
	}

	slices.SortFunc(merged, func(i, j ValidationError) int {
		return i.token.Lno - j.token.Lno
	})

	return merged
}

type entry struct {
	key   *conl.Token
	value conlValue
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
			current.Map = append(current.Map, entry{key: &token})

		case conl.ListItem:
			current.List = append(current.List, entry{key: &token})

		case conl.Scalar, conl.MultilineScalar:
			if len(current.Map) > 0 {
				current.Map[len(current.Map)-1].value = conlValue{Scalar: &token}
			} else {
				current.List[len(current.List)-1].value = conlValue{Scalar: &token}
			}

		case conl.Indent:
			if len(current.Map) > 0 {
				current.Map[len(current.Map)-1].value = conlValue{}
				stack = append(stack, &current.Map[len(current.Map)-1].value)
			} else {
				current.List[len(current.List)-1].value = conlValue{}
				stack = append(stack, &current.List[len(current.List)-1].value)
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
