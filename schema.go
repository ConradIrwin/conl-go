package conl

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

type mapEntry struct {
	Lno   int
	Key   string
	Value conlValue
}

type listEntry struct {
	Lno   int
	Value conlValue
}

type conlValue struct {
	Lno    int
	Scalar *string
	Map    []mapEntry
	List   []listEntry
	Error  *string
}

func parseDoc(input string) *conlValue {
	root := &conlValue{Lno: 1}
	stack := []*conlValue{root}

	for lno, token := range Tokens(input) {
		current := stack[len(stack)-1]
		value := token.Content

		switch token.Kind {
		case MapKey:
			current.Map = append(current.Map, mapEntry{Lno: lno, Key: token.Content})

		case ListItem:
			current.List = append(current.List, listEntry{Lno: lno})

		case Value, MultilineValue:
			if len(current.Map) > 0 {
				current.Map[len(current.Map)-1].Value = conlValue{Lno: lno, Scalar: &value}
			} else {
				current.List[len(current.List)-1].Value = conlValue{Lno: lno, Scalar: &value}
			}

		case NoValue:
			if len(current.Map) > 0 {
				current.Map[len(current.Map)-1].Value = conlValue{Lno: current.Map[len(current.Map)-1].Lno}
			} else {
				current.List[len(current.List)-1].Value = conlValue{Lno: current.List[len(current.List)-1].Lno}
			}

		case Indent:
			if len(current.Map) > 0 {
				current.Map[len(current.Map)-1].Value = conlValue{Lno: current.Map[len(current.Map)-1].Lno}
				stack = append(stack, &current.Map[len(current.Map)-1].Value)
			} else {
				current.List[len(current.List)-1].Value = conlValue{Lno: current.List[len(current.List)-1].Lno}
				stack = append(stack, &current.List[len(current.List)-1].Value)
			}
		case Outdent:
			stack = stack[:len(stack)-1]

		case Error:
			current.Error = &value

		case MultilineHint, Comment:
		default:
			panic(fmt.Errorf("%v: missing case %#v", lno, token))
		}
	}

	return root
}

type matcher struct {
	Pattern   *regexp.Regexp
	Reference string
	Resolved  *node
}

func (m *matcher) Resolve(s Schema, seen []string) error {
	if m.Pattern != nil || m.Resolved != nil {
		return nil
	}
	next, ok := s[m.Reference]
	if !ok {
		return fmt.Errorf("<%s> is not defined", m.Reference)
	} else if slices.Contains(seen, m.Reference) {
		return fmt.Errorf("<%s> is defined in terms of itself", m.Reference)
	}
	if err := next.Resolve(s, m.Reference, append(seen, m.Reference)); err != nil {
		return err
	}
	m.Resolved = next
	return nil
}

func (m *matcher) Validate(s Schema, val *conlValue, key string) (errors []ValidationError) {
	if m.Resolved != nil {
		return m.Resolved.Validate(s, val, key)
	}
	if val.Scalar == nil {
		errors = append(errors,
			ValidationError{
				Lno:           val.Lno,
				expectedMatch: []string{"scalar"},
				key:           key,
			})
		return errors
	}
	if !m.Pattern.MatchString(*val.Scalar) {
		errors = append(errors, ValidationError{
			Lno:           val.Lno,
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

type Schema map[string]*node

type node struct {
	Name string `conl:"-"`
	Docs string `conl:"docs"`

	Scalar *matcher `conl:"pattern"`
	Hint   string   `conl:"hint"`

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

func (n *node) Resolve(s Schema, name string, seen []string) error {

	if n.Name != "" {
		return nil
	}
	count := sumIf(n.Scalar != nil,
		n.OneOf != nil,
		n.Keys != nil || n.RequiredKeys != nil,
		n.Items != nil || n.RequiredItems != nil)

	if count > 1 {
		return fmt.Errorf("invalid schema: %v must have only one of pattern, enum, (required) keys, or (required) items", name)
	}
	if n.Scalar != nil {
		if err := n.Scalar.Resolve(s, seen); err != nil {
			return err
		}
	}
	for _, choice := range n.OneOf {
		if err := choice.Resolve(s, seen); err != nil {
			return err
		}
	}
	for pat, key := range n.Keys {
		if err := pat.Resolve(s, seen); err != nil {
			return err
		}
		if err := key.Resolve(s, seen); err != nil {
			return err
		}
	}
	for pat, key := range n.RequiredKeys {
		if err := pat.Resolve(s, seen); err != nil {
			return err
		}
		if err := key.Resolve(s, seen); err != nil {
			return err
		}
	}
	if n.Items != nil {
		if err := n.Items.Resolve(s, seen); err != nil {
			return err
		}
	}
	for _, item := range n.RequiredItems {
		if err := item.Resolve(s, seen); err != nil {
			return err
		}
	}
	return nil
}

func (n *node) Validate(s Schema, val *conlValue, key string) (errors []ValidationError) {
	if val.Error != nil {
		errors = append(errors,
			ValidationError{
				Lno: val.Lno,
				key: key,
				err: *val.Error,
			})
		return errors
	}

	if n.Scalar != nil {
		if val.Map != nil || val.List != nil {
			errors = append(errors,
				ValidationError{
					Lno:           val.Lno,
					key:           key,
					expectedMatch: []string{"a scalar"},
				})
			return errors
		}
		if err := n.Scalar.Validate(s, val, key); err != nil {
			return err
		}
	}

	if n.OneOf != nil {
		for _, item := range n.OneOf {
			nextErrors := item.Validate(s, val, key)
			if len(nextErrors) == 0 {
				return nil
			}
			if len(errors) == 0 || len(nextErrors) < len(errors) || nextErrors[0].Lno >= errors[0].Lno {
				errors = mergeErrors(nextErrors, errors)
			} else {
				errors = mergeErrors(errors, nextErrors)
			}
		}
		return errors
	}

	if n.Keys != nil || n.RequiredKeys != nil {
		seenRequired := make(map[*matcher]bool)
		if val.Scalar != nil || val.List != nil {
			errors = append(errors,
				ValidationError{
					Lno:           val.Lno,
					key:           key,
					expectedMatch: []string{"a map"},
				})
			return errors
		}

		for _, entry := range val.Map {
			allowed := false
			for keyMatcher, valueMatcher := range n.RequiredKeys {
				keyErrors := keyMatcher.Validate(s, &conlValue{Lno: entry.Lno, Scalar: &entry.Key}, "")
				if len(keyErrors) == 0 {
					seenRequired[keyMatcher] = true
					allowed = true
					errors = append(errors, valueMatcher.Validate(s, &entry.Value, entry.Key)...)
				}
			}
			if !allowed {
				for keyMatcher, valueMatcher := range n.Keys {
					keyErrors := keyMatcher.Validate(s, &conlValue{Lno: entry.Lno, Scalar: &entry.Key}, "")
					if len(keyErrors) == 0 {
						allowed = true
						errors = append(errors, valueMatcher.Validate(s, &entry.Value, entry.Key)...)
						break
					}
				}
			}
			if !allowed {
				errors = append(errors, ValidationError{
					Lno:        entry.Lno,
					key:        key,
					unexpected: fmt.Sprintf("key %s", entry.Key),
				})
			}
		}

		requiredErrors := []ValidationError{}

		for keyMatcher := range n.RequiredKeys {
			if !seenRequired[keyMatcher] {
				errors = append(errors, ValidationError{
					Lno:         val.Lno,
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

	if n.Items != nil || n.RequiredItems != nil {
		if val.Scalar != nil || val.Map != nil {
			errors = append(errors,
				ValidationError{
					Lno:           val.Lno,
					key:           key,
					expectedMatch: []string{"a list"},
				})
			return errors
		}
		for i, valueMatcher := range n.RequiredItems {
			if i < len(val.List) {
				errors = append(errors, valueMatcher.Validate(s, &val.List[i].Value, "")...)
			}
		}
		if len(n.RequiredItems) > len(val.List) {
			errors = append(errors, ValidationError{
				Lno:          val.Lno,
				key:          key,
				requiredItem: fmt.Sprintf("%s", n.RequiredItems[len(val.List)]),
			})
		}
		if n.Items == nil && len(val.List) > len(n.RequiredItems) {
			errors = append(errors, ValidationError{
				Lno:        val.List[len(n.RequiredItems)].Lno,
				key:        key,
				unexpected: "list item",
			})
		}
		if n.Items != nil {
			for i := len(n.RequiredItems); i < len(val.List); i++ {
				errors = append(errors, n.Items.Validate(s, &val.List[i].Value, "")...)
			}
		}
		return errors
	}

	if val.List != nil || val.Map != nil || val.Scalar != nil {
		errors = append(errors,
			ValidationError{
				Lno:           val.Lno,
				key:           key,
				expectedMatch: []string{"no value"},
			})
	}
	return errors

}

func ParseSchema(input []byte) (Schema, error) {
	schema := Schema{}
	if err := Unmarshal(input, &schema); err != nil {
		return nil, err
	}
	if _, ok := schema["root"]; !ok {
		return nil, fmt.Errorf("invalid schema: missing \"root\"")
	}
	for k, v := range schema {
		if err := v.Resolve(schema, k, []string{}); err != nil {
			return nil, err
		}
	}
	return schema, nil
}

// Validate validates the input against the schema.
// If it matches, it returns an empty slice of errors.
// If it does not match, it returns a non-empty slice of errors.
// As there may be multiple possible ways for a schema to match,
// the errors returned are an arbitrary subset of the possible problems.
// The exact errors returned will change over time as heuristics improve.
func (s Schema) Validate(input string) []ValidationError {
	doc := parseDoc(input)
	return s["root"].Validate(s, doc, "")
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
	Lno           int
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

func (ve *ValidationError) Error() string {
	switch true {
	case ve.err != "":
		return fmt.Sprintf("%d: %v", ve.Lno, ve.err)

	case ve.requiredKey != nil:
		return fmt.Sprintf("%d: missing required key %v", ve.Lno, joinWithOr(ve.requiredKey))

	case ve.requiredItem != "":
		return fmt.Sprintf("%d: missing required list item %v", ve.Lno, ve.requiredItem)

	case ve.expectedMatch != nil:
		if ve.key != "" {
			return fmt.Sprintf("%d: expected %s = %v", ve.Lno, ve.key, joinWithOr(ve.expectedMatch))
		} else {
			return fmt.Sprintf("%d: expected %v", ve.Lno, joinWithOr(ve.expectedMatch))
		}

	case ve.unexpected != "":
		return fmt.Sprintf("%d: unexpected %v", ve.Lno, ve.unexpected)

	default:
		panic(fmt.Errorf("unhandled %#v", ve))
	}
}

func mergeErrors(a, b []ValidationError) []ValidationError {
	merged := make([]ValidationError, 0)
	aMap := make(map[int]ValidationError)

	for _, err := range a {
		aMap[err.Lno] = err
	}

	for _, errB := range b {
		if errA, exists := aMap[errB.Lno]; exists {
			merged = append(merged, ValidationError{
				key:           errA.key,
				expectedMatch: append(errB.expectedMatch, errA.expectedMatch...),
				requiredKey:   append(errB.requiredKey, errA.requiredKey...),
				requiredItem:  errA.requiredItem,
				unexpected:    errA.unexpected,
				err:           errA.err,
				Lno:           errA.Lno,
			})
			delete(aMap, errB.Lno)
		}
	}

	for _, errA := range aMap {
		merged = append(merged, errA)
	}

	slices.SortFunc(merged, func(i, j ValidationError) int {
		return i.Lno - j.Lno
	})

	return merged
}
