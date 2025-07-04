// package schema provides a mechanism to validate the structure
// of a CONL document.
package schema

import (
	"fmt"
	"iter"
	"maps"
	"math"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/ConradIrwin/conl-go"
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

// Validate the input against the schema.
// The [Result] can be queried in various ways to determine properties
// of the input document.
func (s *Schema) Validate(input []byte) *Result {
	doc := parseDoc(input)
	result := s.root.validate(doc, resultPos(0))
	return &Result{
		result.raw,
		result.firstErr,
		doc,
		s,
	}
}

// Validate a CONL document. If load is nil (or returns nil) then [Any] is used.
// If the document contains a top-level key 'schema' then its value is passed to load,
// otherwise load is called with "".
// The [Result] can be queried for further information about the match.
func Validate(input []byte, load func(schema string) (*Schema, error)) *Result {
	doc := parseDoc(input)
	var schema *Schema
	var err error
	for _, entry := range doc.Map {
		if entry.key != nil && entry.key.Content == "schema" && entry.value.Scalar != nil {
			schema, err = load(entry.value.Scalar.Content)
			load = nil
			if err != nil {
				entry.value.Scalar.Error = err
			}
			break
		}
	}
	if load != nil {
		schema, err = load("")
		if err != nil {
			for _, entry := range doc.Map {
				if entry.key != nil {
					entry.key.Error = err
					break
				}
			}
		}
	}

	if schema == nil {
		schema = Any()
	}

	r := schema.root.validate(doc, resultPos(0))
	return &Result{
		r.raw,
		r.firstErr,
		doc,
		schema,
	}
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
    any of
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

type definition struct {
	Name string `conl:"-"`
	Docs string `conl:"docs"`

	Scalar *matcher `conl:"scalar"`

	AnyOf []*matcher `conl:"any of"`

	Keys         map[*matcher]*matcher `conl:"keys"`
	RequiredKeys map[*matcher]*matcher `conl:"required keys"`

	Items         *matcher   `conl:"items"`
	RequiredItems []*matcher `conl:"required items"`
}

type valueMatcher struct {
	pattern   *regexp.Regexp
	reference string
	resolved  *definition
	raw       string
}

func (v *valueMatcher) UnmarshalText(data []byte) error {
	if data[0] == '<' {
		if data[len(data)-1] != '>' {
			return fmt.Errorf("missing closing >")
		}
		v.reference = string(data[1 : len(data)-1])
		v.raw = string(data)
		return nil
	}
	pattern := &regexp.Regexp{}
	if err := pattern.UnmarshalText([]byte("(?s)^" + string(data) + "$")); err != nil {
		return err
	}
	v.pattern = pattern
	v.raw = string(data)
	return nil
}

type matcher struct {
	valueMatcher
	Docs string
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
		return m.UnmarshalText([]byte(token.Content))
	case conl.NoValue:
		return fmt.Errorf("%d: missing matcher", token.Lno)
	default:
		type docsMatcher struct {
			Matches valueMatcher `conl:"matches"`
			Docs    string       `conl:"docs"`
		}

		t := docsMatcher{}
		err := conl.UnmarshalCONL(tokens, &t)
		if err != nil {
			return err
		}
		m.valueMatcher = t.Matches
		m.Docs = t.Docs
		return nil
	}
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

func (m *matcher) resolve(s *Schema, seen []string) error {
	if m.pattern != nil || m.resolved != nil {
		return nil
	}
	d, ok := s.definitions[m.reference]
	if !ok {
		return fmt.Errorf("<%s> is not defined", m.reference)
	} else if slices.Contains(seen, m.reference) {
		return fmt.Errorf("<%s> is defined in terms of itself", m.reference)
	} else if d == nil {
		d = &definition{}
	}
	m.resolved = d

	if d.Name != "" {
		return nil
	}
	count := sumIf(d.Scalar != nil,
		d.AnyOf != nil,
		d.Keys != nil || d.RequiredKeys != nil,
		d.Items != nil || d.RequiredItems != nil)

	if count > 1 {
		return fmt.Errorf("invalid definition %v: cannot mix scalar, any of, (required) keys, and (required) items", d.Name)
	}
	if d.Scalar != nil {
		if err := d.Scalar.resolve(s, seen); err != nil {
			return err
		}
	}
	for _, choice := range d.AnyOf {
		if err := choice.resolve(s, seen); err != nil {
			return err
		}
	}
	for key, val := range d.Keys {
		if err := key.resolve(s, nil); err != nil {
			return err
		}
		if err := val.resolve(s, nil); err != nil {
			return err
		}
		key.Docs = val.Docs
	}
	for key, val := range d.RequiredKeys {
		if err := key.resolve(s, nil); err != nil {
			return err
		}
		if err := val.resolve(s, nil); err != nil {
			return err
		}
		key.Docs = val.Docs
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

func (m *matcher) validate(val *conlValue, pos resultPos) result {
	d := m.resolved

	if d == nil {
		if val.Scalar == nil || val.Scalar.Error != nil {
			return newResult(pos, valueAttempt(val, false, m))
		}
		if !m.pattern.MatchString(val.Scalar.Content) {
			return newResult(pos, valueAttempt(val, false, m))
		}
		return newResult(pos, valueAttempt(val, true, m))
	}

	if d.Scalar != nil {
		return d.Scalar.validate(val, pos)
	}

	if d.AnyOf != nil {
		var combined result
		for _, matcher := range d.AnyOf {
			result := matcher.validate(val, pos)
			combined = pickBestResult(combined, result)
		}
		return combined
	}

	if d.Items != nil || d.RequiredItems != nil {
		if val.Scalar != nil || val.Map != nil {
			return newResult(pos, valueAttempt(val, false, m))
		}

		combined := newResult(pos, valueAttempt(val, len(val.List) >= len(d.RequiredItems), m))
		for ix, entry := range val.List {
			if entry.key.Error != nil {
				combined = appendResult(combined, posForKey(entry.key.Lno), keyAttempt(entry, false, nil))
				continue
			}
			combined = appendResult(combined, posForKey(entry.key.Lno), keyAttempt(entry, true, nil))
			if ix < len(d.RequiredItems) {
				itemResult := d.RequiredItems[ix].validate(entry.value, posForValue(entry.key.Lno))
				combined = appendAllResults(combined, itemResult)
			} else if d.Items != nil {
				itemResult := d.Items.validate(entry.value, posForValue(entry.key.Lno))
				combined = appendAllResults(combined, itemResult)
			} else {
				combined = appendResult(combined, posForKey(entry.key.Lno), keyAttempt(entry, false, nil))
			}
		}
		return combined
	}

	if d.Keys != nil || d.RequiredKeys != nil {
		if val.Scalar != nil || val.List != nil {
			return newResult(pos, valueAttempt(val, false, m))
		}

		seen := map[string]bool{}
		seenRequired := map[*matcher]bool{}
		var combined result

	outer:
		for _, entry := range val.Map {
			if seen[entry.key.Content] {
				combined = appendResult(combined, posForKey(entry.key.Lno), keyAttempt(entry, false, nil))
				continue
			}
			seen[entry.key.Content] = true
			anyOf := []*matcher{}
			var keyResult result
			for k, v := range d.RequiredKeys {
				keyResult = pickBestResult(keyResult, k.validate(&conlValue{Scalar: entry.key}, posForKey(entry.key.Lno)))
				if keyResult.firstErr == success {
					if seenRequired[k] {
						attempt := keyAttempt(entry, false, nil).withDuplicate(k)
						combined = appendResult(combined, posForKey(entry.key.Lno), attempt)
						continue outer
					}
					seenRequired[k] = true
					anyOf = append(anyOf, v)
					break
				}
			}

			if len(anyOf) == 0 {
				for k, v := range d.Keys {
					newResult := k.validate(&conlValue{Scalar: entry.key}, posForKey(entry.key.Lno))
					if newResult.firstErr == success {
						anyOf = append(anyOf, v)
					}
					keyResult = pickBestResult(keyResult, newResult)
				}
			}
			combined = appendAllResults(combined, keyResult)
			if len(anyOf) == 0 {
				continue
			}

			var valueResult result
			for _, matcher := range anyOf {
				result := matcher.validate(entry.value, posForValue(entry.key.Lno))
				valueResult = pickBestResult(valueResult, result)
			}

			combined = appendAllResults(combined, valueResult)
		}

		missing := []*matcher{}
		for k, _ := range d.RequiredKeys {
			if !seenRequired[k] {
				missing = append(missing, k)
				break
			}
		}
		attempt := valueAttempt(val, len(missing) == 0, m).withMissingKeys(missing)
		result := appendResult(combined, pos, attempt)

		return result
	}

	matches := val.Scalar == nil && val.List == nil && val.Map == nil
	return newResult(pos, valueAttempt(val, matches, m))
}

func (m *matcher) suggestedValues() []*Suggestion {
	var suggestions []*Suggestion
	if m.resolved == nil {
		for _, s := range suggestionsFromPattern(m.raw, false) {
			suggestions = append(suggestions, &Suggestion{
				Value: s,
				Docs:  m.Docs,
			})
		}
		return suggestions
	}

	d := m.resolved

	if d.Scalar != nil {
		suggestions = append(suggestions, d.Scalar.suggestedValues()...)
	}

	for _, anyOf := range d.AnyOf {
		suggestions = append(suggestions, anyOf.suggestedValues()...)
	}

	for _, s := range suggestions {
		if s.Docs == "" {
			s.Docs = m.Docs
		}
	}
	return suggestions
}

type resultPos int

var success = math.MaxInt

func (rp resultPos) isKey() bool {
	return rp < 0
}

func (rp resultPos) Lno() int {
	if rp < 0 {
		return -int(rp)
	}
	return int(rp)
}

func posForKey(lno int) resultPos {
	return resultPos(-lno)
}

func posForValue(lno int) resultPos {
	return resultPos(lno)
}

type result struct {
	raw      map[resultPos][]*attempt
	firstErr int
	errCount int
}

func newResult(pos resultPos, att *attempt) result {
	score := success
	errCount := 0
	if !att.ok {
		score = pos.Lno()
		errCount = 1
	}

	return result{
		raw: map[resultPos][]*attempt{
			pos: {att},
		},
		firstErr: score,
		errCount: errCount,
	}
}

func appendResult(r result, pos resultPos, att *attempt) result {
	if r.raw == nil {
		return newResult(pos, att)
	}

	if !att.ok && r.raw[pos] == nil {
		r.errCount++
	}
	r.raw[pos] = append(r.raw[pos], att)
	if !att.ok && (pos.Lno() > r.firstErr || r.firstErr == success) {
		r.firstErr = pos.Lno()
	}
	return r
}

func appendAllResults(r1, r2 result) result {
	for pos, v := range r2.raw {
		for _, m := range v {
			r1 = appendResult(r1, pos, m)
		}
	}
	return r1
}

func pickBestResult(r1, r2 result) result {
	if r1.raw == nil {
		return r2
	} else if r2.raw == nil {
		return r1
	}
	if r1.firstErr > r2.firstErr {
		return r1
	} else if r2.firstErr > r1.firstErr {
		return r2
	}
	if r1.errCount < r2.errCount {
		return r1
	} else if r2.errCount < r1.errCount {
		return r2
	}
	return appendAllResults(r1, r2)
}

type attempt struct {
	matcher     *matcher
	val         *conlValue
	ok          bool
	missingKeys []*matcher
	duplicate   *matcher
	parentLno   int
}

func valueAttempt(val *conlValue, ok bool, matcher *matcher) *attempt {
	return &attempt{
		matcher: matcher,
		val:     val,
		ok:      ok,
	}
}

func keyAttempt(entry entry, ok bool, matcher *matcher) *attempt {
	return &attempt{
		matcher:   matcher,
		val:       &conlValue{Scalar: entry.key},
		parentLno: entry.parentLno,
		ok:        ok,
	}
}

func (a *attempt) withMissingKeys(missing []*matcher) *attempt {
	a.missingKeys = missing
	return a
}

func (a *attempt) withDuplicate(dup *matcher) *attempt {
	a.duplicate = dup
	return a
}

// A Result is produced when validating a document against a [Schema].
type Result struct {
	raw    map[resultPos][]*attempt
	score  int
	doc    *conlValue
	schema *Schema
}

// Valid returns true if the document matches the schema.
// The result may still be queried for other properties even if the
// document was not valid.
func (r *Result) Valid() bool {
	return r.score == success
}

// Errors returns a non-empty list of errors if the result is not valid.
// As there are many potential ways for a schema to match a document, the
// exact errors returned may change over time.
func (r *Result) Errors() []ValidationError {
	if r.score == success {
		return nil
	}
	result := []ValidationError{}
	for pos, ms := range r.raw {
		if ve := validationError(pos, ms); ve.msg != "" {
			result = append(result, ve)
		}
	}
	slices.SortFunc(result, func(a ValidationError, b ValidationError) int {
		if a.Lno() == b.Lno() {
			return int(a.pos - b.pos)
		}
		return a.Lno() - b.Lno()
	})
	return result
}

// SuggestedKeys returns possible keys for the map defined on line,
// or for the root of the document if line == 0.
// If the value defined on this line is a list, "=" is returned.
func (r *Result) SuggestedKeys(line int) []*Suggestion {
	possible := []*matcher{}
	listAllowed := false
	var val *conlValue
	for _, m := range r.raw[posForValue(line)] {
		if m.matcher == nil || m.matcher.resolved == nil {
			continue
		}
		d := m.matcher.resolved
		val = m.val
		possible = slices.AppendSeq(possible, maps.Keys(d.Keys))
		possible = slices.AppendSeq(possible, maps.Keys(d.RequiredKeys))
		if d.Items != nil || d.RequiredItems != nil {
			listAllowed = true
		}
	}

	if val != nil {
		for ix, p := range possible {
			for _, entry := range val.Map {
				if p.validate(&conlValue{Scalar: entry.key}, posForKey(entry.key.Lno)).errCount == 0 {
					possible[ix] = nil
				}
			}
		}
	}

	results := []*Suggestion{}

	for _, p := range possible {
		if p == nil {
			continue
		}

		results = append(results, p.suggestedValues()...)
	}
	if listAllowed {
		results = append(results, &Suggestion{Value: "=", Docs: ""})
	}
	slices.SortFunc(results, func(a, b *Suggestion) int {
		return strings.Compare(a.Value, b.Value)
	})
	results = slices.CompactFunc(results, func(a, b *Suggestion) bool {
		return a.Value == b.Value
	})

	return results
}

// DocsForKey returns the docs for the key on line (1-based)
func (r *Result) DocsForKey(line int) string {
	for _, attempt := range r.raw[posForKey(line)] {
		if attempt.matcher != nil && attempt.ok {
			return attempt.matcher.Docs
		}
	}
	return ""
}

// DocsForValue returns the docs for the value on line (1-based)
func (r *Result) DocsForValue(lno int) string {
	for _, attempt := range r.raw[posForValue(lno)] {
		if attempt.matcher != nil && attempt.ok {
			return attempt.matcher.Docs
		}
	}
	return ""
}

// SuggestedValues returns possible values to autocomplete on line (1-based)
func (r *Result) SuggestedValues(line int) []*Suggestion {
	possible := []*matcher{}
	var key *conlValue
	var parentLno int

	for _, m := range r.raw[posForKey(line)] {
		parentLno = m.parentLno
		key = m.val
		break
	}

	if key == nil {
		return nil
	}

	for _, m := range r.raw[posForValue(parentLno)] {
		if m.matcher == nil || m.matcher.resolved == nil {
			continue
		}
		d := m.matcher.resolved
		if key.Scalar.Kind == conl.ListItem {
			for ix, e := range m.val.List {
				if e.key == key.Scalar {
					if ix < len(d.RequiredItems) {
						possible = append(possible, d.RequiredItems[ix])
					} else if d.Items != nil {
						possible = append(possible, d.Items)
					}
				}
			}
			continue
		}

		for k, v := range d.RequiredKeys {
			if k.validate(key, posForKey(line)).errCount == 0 {
				possible = append(possible, v)
			}
		}
		for k, v := range d.Keys {
			if k.validate(key, posForKey(line)).errCount == 0 {
				possible = append(possible, v)
			}
		}

	}

	results := []*Suggestion{}

	for _, p := range possible {
		if p == nil {
			continue
		}
		results = append(results, p.suggestedValues()...)
	}
	slices.SortFunc(results, func(a, b *Suggestion) int {
		return strings.Compare(a.Value, b.Value)
	})
	results = slices.CompactFunc(results, func(a, b *Suggestion) bool {
		return a.Value == b.Value
	})

	return results
}

// Suggestion is returned by [Result.SuggestedKeys] or [Result.SuggestedValues]
type Suggestion struct {
	Value string
	Docs  string
}

func suggestionsFromPattern(pattern string, raw bool) []string {
	if strings.ContainsAny(pattern, ".\\[](){}^$?*+") {
		if raw {
			return []string{pattern}
		}
		return nil
	}
	return strings.Split(pattern, "|")
}
