// package schema provides a mechanism to validate the structure
// of a CONL document.
package schema

import (
	"fmt"
	"iter"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/ConradIrwin/conl-go"
	"github.com/ConradIrwin/dbg"
)

// A Schema allows you to validate a CONL document against a set of rules.
type Schema struct {
	root        *matcher
	definitions map[string]*definition
}

// Parse a schema from the given input.
// An error is returned if the input is not valid CONL,
// or if the schema contains references to definitions that don't exist,
// invalid regular expressions, or circular references.
func Parse(input []byte) (*Schema, error) {
	type tempSchema struct {
		Root        *matcher               `conl:"root"`
		Schema      string                 `conl:"schema"`
		Definitions map[string]*definition `conl:"definitions"`
	}
	t := &tempSchema{}
	if err := conl.Unmarshal(input, &t); err != nil {
		return nil, err
	}
	s := &Schema{root: t.Root, definitions: t.Definitions}
	if s.root == nil {
		return nil, fmt.Errorf("missing root")
	}
	if err := s.root.resolve(s, []string{}); err != nil {
		return nil, err
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
	rootToken := &conl.Token{Lno: 1, Kind: conl.NoValue}
	guesses, errors := s.root.validate(s, doc, rootToken)
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
	guesses   map[*conl.Token][]*matcher
	schema    *Schema
}

func (r *Result) Valid() bool {
	return len(r.errors) == 0
}

func (r *Result) Errors() []ValidationError {
	return r.errors
}

func (r *Result) matchersForLine(lno int) []*matcher {
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
func (r *Result) SuggestedKeys(lno int) []*Suggestion {
	definitions := []*definition{}
	if lno == 0 {
		if r.schema.root.Matches.Resolved != nil {
			definitions = append(definitions, r.schema.root.Matches.Resolved)
		}
	} else {
		matchers := r.matchersForLine(lno)
		for _, m := range matchers {
			if m.Matches.Resolved != nil {
				definitions = append(definitions, m.Matches.Resolved)
			}
		}
	}
	possible := []matcherPair{}
	listAllowed := false
	for _, definition := range definitions {
		p, l := definition.suggestedKeys()
		possible = append(possible, p...)
		listAllowed = listAllowed || l
	}

	for i, m := range possible {
		for _, entry := range r.doc.Map {
			_, keyErrors := m.key.validate(r.schema, &conlValue{Scalar: entry.key}, entry.key)
			if len(keyErrors) == 0 {
				possible[i] = matcherPair{}
				break
			}
		}
	}
	results := []*Suggestion{}
	for _, m := range possible {
		if m.key == nil {
			continue
		}
		possible, _ := m.key.suggestedValues()
		for _, p := range possible {
			p.Docs = m.value.Docs
		}
		results = append(results, possible...)
	}
	if listAllowed {
		results = append(results, &Suggestion{Value: "="})
	}
	slices.SortFunc(results, func(a *Suggestion, b *Suggestion) int {
		return strings.Compare(a.Value, b.Value)
	})
	return results
}

// SuggestedValues returns possible values for the key on line `lno`
// (or for the root of the document if lno == 0)
// If the value may be a list or an object, "\n  " is included in the response.
func (r *Result) SuggestedValues(lno int) ([]*Suggestion, bool) {
	possible := []*Suggestion{}
	indentAllowed := false
	if lno == 0 {
		p, i := r.schema.root.suggestedValues()
		possible = append(possible, p...)
		indentAllowed = indentAllowed || i
	} else {
		for _, matcher := range r.matchersForLine(lno) {
			p, i := matcher.suggestedValues()
			possible = append(possible, p...)
			indentAllowed = indentAllowed || i
		}
	}
	slices.SortFunc(possible, func(a *Suggestion, b *Suggestion) int {
		return strings.Compare(a.Value, b.Value)
	})
	return possible, indentAllowed
}

type Suggestion struct {
	Value string
	Docs  string
}

var anySchema *Schema
var once sync.Once

// Any is a schema that validates any CONL document.
func Any() *Schema {
	once.Do(func() {
		sch, err := Parse([]byte(`
root = <any>
definitions
  any
    one of
      = <map>
      = <list>
      = .*
  list
    items = <any>
  map
    keys
     .* = <any>
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

	guesses, errors := schema.root.validate(schema, doc, rootToken)
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

func (d *definition) validate(s *Schema, val *conlValue, pos *conl.Token) (map[*conl.Token][]*matcher, []ValidationError) {
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
		return withItem(guesses, pos, []*matcher{d.Scalar}), errors
	}

	if d.OneOf != nil {
		var bestGuesses map[*conl.Token][]*matcher
		var errors []ValidationError
		for _, item := range d.OneOf {
			guesses, nextErrors := item.validate(s, val, pos)
			if len(nextErrors) == 0 {
				return withItem(guesses, pos, []*matcher{item}), nil
			}
			if len(errors) > 0 && (nextErrors[0].Lno() < errors[0].Lno() || nextErrors[0].Lno() == errors[0].Lno() && len(nextErrors) > len(errors)) {
				errors = mergeErrors(errors, nextErrors)
			} else {
				if len(errors) > 0 && nextErrors[0].Lno() == errors[0].Lno() {
					bestGuesses = mergeGuesses(bestGuesses, guesses)
				} else {
					bestGuesses = withItem(guesses, pos, []*matcher{item})
				}
				errors = mergeErrors(nextErrors, errors)
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
		guesses := map[*conl.Token][]*matcher{}

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
							duplicateKey: keyMatcher.Matches.String(),
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
			var bestGuesses map[*conl.Token][]*matcher
			for _, item := range oneOf {
				guesses, nextErrors := item.validate(s, &entry.value, entry.key)
				if len(nextErrors) == 0 {
					bestGuesses = withItem(guesses, entry.key, []*matcher{item})
					itemErrors = nil
					break
				}

				if len(itemErrors) > 0 && (nextErrors[0].Lno() < itemErrors[0].Lno() || nextErrors[0].Lno() == itemErrors[0].Lno() && len(nextErrors) > len(itemErrors)) {
					itemErrors = mergeErrors(itemErrors, nextErrors)
				} else {
					if len(itemErrors) > 0 && nextErrors[0].Lno() == itemErrors[0].Lno() {
						bestGuesses = mergeGuesses(bestGuesses, guesses)
					} else {
						bestGuesses = withItem(guesses, entry.key, []*matcher{item})
					}
					itemErrors = mergeErrors(nextErrors, itemErrors)
				}
			}
			for k, v := range bestGuesses {
				guesses[k] = v
			}
			errors = append(errors, itemErrors...)
		}

		for keyMatcher := range d.RequiredKeys {
			if !seenRequired[keyMatcher] {
				dbg.Dbg(pos, keyMatcher.Matches.String())
				errors = append(errors, ValidationError{
					token:       pos,
					requiredKey: []string{keyMatcher.Matches.String()},
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
		guesses := map[*conl.Token][]*matcher{}

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
				guesses[entry.key] = []*matcher{valueMatcher}
				for k, v := range more {
					guesses[k] = v
				}
				errors = append(errors, errs...)
			}
		}
		if len(d.RequiredItems) > len(val.List) {
			errors = append(errors, ValidationError{
				token:        pos,
				requiredItem: d.RequiredItems[len(val.List)].Matches.String(),
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
				guesses[entry.key] = []*matcher{d.Items}
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

type matcherPair struct {
	key   *matcher
	value *matcher
}

func (d *definition) suggestedKeys() ([]matcherPair, bool) {
	possible := []matcherPair{}
	listAllowed := false
	for key, value := range d.RequiredKeys {
		possible = append(possible, matcherPair{key, value})
	}
	for key, value := range d.Keys {
		possible = append(possible, matcherPair{key, value})
	}
	for _, oneOf := range d.OneOf {
		if oneOf.Matches.Resolved != nil {
			more, allowed := oneOf.Matches.Resolved.suggestedKeys()
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

func (d *definition) suggestedValues() ([]*Suggestion, bool) {
	var possible []*Suggestion
	indentAllowed := false
	if len(d.RequiredKeys) > 0 || len(d.Keys) > 0 || len(d.RequiredItems) > 0 || d.Items != nil {
		indentAllowed = true
	}
	if d.Scalar != nil {
		p, i := d.Scalar.suggestedValues()
		possible = append(possible, p...)
		indentAllowed = indentAllowed || i
	}

	for _, oneOf := range d.OneOf {
		p, i := oneOf.suggestedValues()
		possible = append(possible, p...)
		indentAllowed = indentAllowed || i
	}
	return possible, indentAllowed
}

type valueMatcher struct {
	Pattern   *regexp.Regexp
	Reference string
	Resolved  *definition
}

type matcher struct {
	Matches valueMatcher `conl:"matches"`
	Docs    string       `conl:"docs"`
}

func (m *matcher) resolve(s *Schema, seen []string) error {
	if m.Matches.Pattern != nil || m.Matches.Resolved != nil {
		return nil
	}
	next, ok := s.definitions[m.Matches.Reference]
	if !ok {
		return fmt.Errorf("<%s> is not defined", m.Matches.Reference)
	} else if slices.Contains(seen, m.Matches.Reference) {
		return fmt.Errorf("<%s> is defined in terms of itself", m.Matches.Reference)
	}
	m.Matches.Resolved = next
	if err := next.resolve(s, m.Matches.Reference, append(seen, m.Matches.Reference)); err != nil {
		return err
	}
	return nil
}

func (m *matcher) validate(s *Schema, val *conlValue, pos *conl.Token) (map[*conl.Token][]*matcher, []ValidationError) {
	if m.Matches.Resolved != nil {
		return m.Matches.Resolved.validate(s, val, pos)
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
	if !m.Matches.Pattern.MatchString(val.Scalar.Content) {
		return nil, []ValidationError{{
			token:         val.Scalar,
			expectedMatch: []string{m.Matches.String()},
		}}
	}
	return nil, nil
}

func (m *matcher) suggestedValues() ([]*Suggestion, bool) {
	if m.Matches.Resolved != nil {
		return m.Matches.Resolved.suggestedValues()
	}
	pat := m.Matches.String()
	if strings.ContainsAny(pat, ".\\[](){}^$?*+") {
		return nil, false
	}
	values := strings.Split(pat, "|")
	suggestions := make([]*Suggestion, len(values))
	for i, v := range values {
		suggestions[i] = &Suggestion{
			Value: v,
			Docs:  m.Docs,
		}
	}

	return suggestions, false
}

func (v *valueMatcher) UnmarshalText(data []byte) error {
	if data[0] == '<' {
		if data[len(data)-1] != '>' {
			return fmt.Errorf("missing closing >")
		}
		v.Reference = string(data[1 : len(data)-1])
		return nil
	}
	pattern := &regexp.Regexp{}
	if err := pattern.UnmarshalText([]byte("(?s)^" + string(data) + "$")); err != nil {
		return err
	}
	v.Pattern = pattern
	return nil
}

func peek(tokens iter.Seq[conl.Token]) conl.Token {
	for tok := range tokens {
		return tok
	}
	return conl.Token{}
}

func (m *matcher) UnmarshalCONL(tokens iter.Seq[conl.Token]) error {
	token := peek(tokens)
	switch token.Kind {
	case conl.Scalar:
		return m.Matches.UnmarshalText([]byte(token.Content))
	case conl.NoValue:
		return fmt.Errorf("%d: missing matcher", token.Lno)
	default:
		type tmp matcher
		t := tmp{}
		err := conl.UnmarshalCONL(tokens, &t)
		if err != nil {
			return err
		}
		*m = (matcher)(t)
		return nil
	}
}

func (v *valueMatcher) String() string {
	if v.Pattern != nil {
		s := v.Pattern.String()
		s = s[5 : len(s)-1]
		if s[0] == '<' {
			s = "\\" + s
		}
		return s
	}
	return "<" + v.Reference + ">"
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

func mergeGuesses(a, b map[*conl.Token][]*matcher) map[*conl.Token][]*matcher {
	for token, value := range b {
		a[token] = append(a[token], value...)
	}
	return a
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
