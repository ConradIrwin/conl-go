package conl

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/davecgh/go-spew/spew"
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
				current.Map[len(current.Map)-1].Value = conlValue{Lno: lno}
			} else {
				current.List[len(current.List)-1].Value = conlValue{Lno: lno}
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

type schemaValue struct {
	Pattern   *regexp.Regexp
	Reference string
	Resolved  *SchemaNode
}

func (sv *schemaValue) Resolve(s Schema, seen []string) error {
	if sv.Pattern != nil || sv.Resolved != nil {
		return nil
	}
	next, ok := s[sv.Reference]
	if !ok {
		return fmt.Errorf("<%s> is not defined", sv.Reference)
	} else if slices.Contains(seen, sv.Reference) {
		return fmt.Errorf("<%s> is defined in terms of itself", sv.Reference)
	}
	if err := next.Resolve(s, sv.Reference, append(seen, sv.Reference)); err != nil {
		return err
	}
	sv.Resolved = next
	return nil
}

func (sv *schemaValue) Validate(s Schema, val *conlValue, key string) (errors []ValidationError) {
	if sv.Resolved != nil {
		return sv.Resolved.Validate(s, val, key)
	}
	if val.Scalar == nil {
		errors = append(errors,
			ValidationError{
				Lno:           val.Lno,
				ExpectedMatch: []string{"scalar"},
				Key:           key,
			})
		return errors
	}
	if !sv.Pattern.MatchString(*val.Scalar) {
		errors = append(errors, ValidationError{
			Lno:           val.Lno,
			Key:           key,
			ExpectedMatch: []string{sv.String()},
		})
		return errors
	}
	return nil
}

func (o *schemaValue) UnmarshalText(data []byte) error {
	if data[0] == '<' {
		if data[len(data)-1] != '>' {
			return fmt.Errorf("missing closing >")
		}
		o.Reference = string(data[1 : len(data)-1])
		return nil
	}
	pattern := &regexp.Regexp{}
	if err := pattern.UnmarshalText([]byte("^" + string(data) + "$")); err != nil {
		return err
	}
	o.Pattern = pattern
	return nil
}

func (o *schemaValue) String() string {
	if o.Pattern != nil {
		s := o.Pattern.String()
		s = s[1 : len(s)-1]
		if s[0] == '<' {
			s = "\\" + s
		}
		return s
	}
	return "<" + o.Reference + ">"
}

func (sv *schemaValue) MarshalText() ([]byte, error) {
	return []byte(sv.String()), nil
}

type Schema map[string]*SchemaNode

type SchemaNode struct {
	Name string `conl:"-"`
	Docs string `conl:"docs"`

	Pattern *schemaValue `conl:"pattern"`
	Hint    string       `conl:"hint"`

	NoValue bool           `conl:"no value"`
	OneOf   []*schemaValue `conl:"one of"`

	Keys         map[*schemaValue]*schemaValue `conl:"keys"`
	RequiredKeys map[*schemaValue]*schemaValue `conl:"required keys"`

	Items         *schemaValue   `conl:"items"`
	RequiredItems []*schemaValue `conl:"required items"`
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

func (n *SchemaNode) Resolve(s Schema, name string, seen []string) error {

	if n.Name != "" {
		return nil
	}
	count := sumIf(n.Pattern != nil,
		n.OneOf != nil,
		n.NoValue,
		n.Keys != nil || n.RequiredKeys != nil,
		n.Items != nil || n.RequiredItems != nil)

	if count == 0 {
		return fmt.Errorf("invalid schema: %v matches nothing", name)
	}
	if count > 1 {
		return fmt.Errorf("invalid schema: %v must have only one of pattern, enum, (required) keys, or (required) items", name)
	}
	if n.Pattern != nil {
		if err := n.Pattern.Resolve(s, seen); err != nil {
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
		if err := n.Pattern.Resolve(s, seen); err != nil {
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

type ValidationError struct {
	Key           string
	ExpectedMatch []string
	RequiredKey   []string
	Unexpected    string
	Err           string
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
	case ve.Err != "":
		return fmt.Sprintf("%d: %v", ve.Lno, ve.Err)

	case ve.ExpectedMatch != nil:
		if ve.Key != "" {
			return fmt.Sprintf("%d: expected %s to match %v", ve.Lno, ve.Key, joinWithOr(ve.ExpectedMatch))
		} else {
			return fmt.Sprintf("%d: expected %v", ve.Lno, joinWithOr(ve.ExpectedMatch))

		}

	case ve.RequiredKey != nil:
		return fmt.Sprintf("%d: missing required key %v", ve.Lno, joinWithOr(ve.RequiredKey))

	case ve.Unexpected != "":
		return fmt.Sprintf("%d: unexpected %v", ve.Lno, ve.Unexpected)

	default:
		panic(fmt.Errorf("unhandled %#v", ve))
	}
}

func mergeErrors(a, b []ValidationError) []ValidationError {
	spew.Dump(a, b)
	merged := make([]ValidationError, 0)
	aMap := make(map[int]ValidationError)

	for _, err := range a {
		aMap[err.Lno] = err
	}

	for _, errB := range b {
		if errA, exists := aMap[errB.Lno]; exists {
			merged = append(merged, ValidationError{
				Key:           errA.Key,
				ExpectedMatch: append(errB.ExpectedMatch, errA.ExpectedMatch...),
				RequiredKey:   append(errB.RequiredKey, errA.RequiredKey...),
				Unexpected:    errA.Unexpected,
				Err:           errA.Err,
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

func (n *SchemaNode) Validate(s Schema, val *conlValue, key string) (errors []ValidationError) {
	if val.Error != nil {
		errors = append(errors,
			ValidationError{
				Lno: val.Lno,
				Key: key,
				Err: *val.Error,
			})
		return errors
	}

	if n.Pattern != nil {
		if err := n.Pattern.Validate(s, val, key); err != nil {
			return err
		}
	}

	if n.OneOf != nil {
		for _, item := range n.OneOf {
			nextErrors := item.Validate(s, val, key)
			if len(nextErrors) == 0 {
				return nil
			}
			if len(errors) == 0 || len(nextErrors) <= len(errors) {
				errors = mergeErrors(nextErrors, errors)
			}
		}
		return errors
	}

	if n.Keys != nil || n.RequiredKeys != nil {
		seenRequired := make(map[*schemaValue]bool)
		if val.Map == nil {
			errors = append(errors,
				ValidationError{
					Lno:           val.Lno,
					Key:           key,
					ExpectedMatch: []string{"map"},
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
			for keyMatcher, valueMatcher := range n.Keys {
				keyErrors := keyMatcher.Validate(s, &conlValue{Lno: entry.Lno, Scalar: &entry.Key}, "")
				if len(keyErrors) == 0 {
					allowed = true
					errors = append(errors, valueMatcher.Validate(s, &entry.Value, entry.Key)...)
					break
				}
			}
			if !allowed {
				errors = append(errors, ValidationError{
					Lno:        entry.Lno,
					Key:        key,
					Unexpected: fmt.Sprintf("key %s", entry.Key),
				})
			}
		}

		requiredErrors := []ValidationError{}

		for keyMatcher := range n.RequiredKeys {
			if !seenRequired[keyMatcher] {
				errors = append(errors, ValidationError{
					Lno:         val.Lno,
					Key:         key,
					RequiredKey: []string{keyMatcher.String()},
				})
			}
		}
		if len(requiredErrors) > 0 {
			spew.Dump(".><")
			spew.Dump(requiredErrors)
			return requiredErrors
		}
		spew.Dump(".<>")
		spew.Dump(requiredErrors)
		return errors
	}

	if n.NoValue {
		if val.List != nil || val.Map != nil || val.Scalar != nil {
			errors = append(errors,
				ValidationError{
					Lno:           val.Lno,
					Key:           key,
					ExpectedMatch: []string{"no value"},
				})
		}
		return errors
	}

	panic("unhandled")
}

func (s Schema) Validate(input string) []ValidationError {
	doc := parseDoc(input)
	return s["root"].Validate(s, doc, "")
}
