package conl

import (
	"encoding"
	"encoding/base64"
	"fmt"
	"iter"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

func requiresQuote(r rune) bool {
	return r <= 0x1f || r == ';' || r == '='
}

func quoteString(s string) string {
	if !(strings.ContainsFunc(s, requiresQuote) || len(s) == 0 || s[0] == '"' || s[0] == ' ' || s[len(s)-1] == ' ') {
		return s
	}

	r := "\""
	for _, c := range s {
		switch {
		case c == '\\':
			r += "\\\\"
		case c == '"':
			r += "\\\""
		case c == '\n':
			r += "\\n"
		case c == '\r':
			r += "\\r"
		case c == '\t':
			r += "\\t"
		case unicode.IsControl(c):
			r += fmt.Sprintf("\\{%02X}", c)
		default:
			r += string(c)
		}

	}
	r += "\""
	return r
}

func quoteValue(s string, indent, hint string) string {
	if s == "" || strings.Contains(s, "\r") || unicode.IsSpace(rune(s[0])) || unicode.IsSpace(rune(s[len(s)-1])) {
		return quoteString(s)
	}
	if hint == "" && !strings.Contains(s, "\n") {
		return quoteString(s)
	}

	return "\"\"\"" + hint + "\n" + indent + strings.ReplaceAll(s, "\n", "\n"+indent)
}

func marshalKey(v any) (string, error) {
	if m, ok := v.(encoding.TextMarshaler); ok {
		if text, err := m.MarshalText(); err == nil {
			return quoteString(string(text)), nil
		} else {
			return "", err
		}
	}

	val := reflect.ValueOf(v)
	val.IsValid()
	switch val.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !val.IsNil() {
			return marshalKey(val.Elem().Interface())
		}
	case reflect.Array:
		if val.Type().Elem().Kind() == reflect.Uint8 {
			return base64.RawStdEncoding.EncodeToString(val.Bytes()), nil
		}
	case reflect.String:
		return quoteString(val.String()), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64, reflect.Complex64, reflect.Complex128, reflect.Bool:
		return fmt.Sprint(v), nil
	}
	return "", fmt.Errorf("unsupported map key type: %s", val.Type())
}

func marshalValue(v any, indent, hint string) (string, bool, error) {
	val := reflect.ValueOf(v)

	if m, ok := v.(encoding.TextMarshaler); ok {
		if text, err := m.MarshalText(); err == nil {
			return quoteValue(string(text), indent+"  ", hint), true, nil
		} else {
			return "", false, err
		}
	}

	switch val.Kind() {
	case reflect.Pointer, reflect.Interface:
		if val.IsNil() {
			return " ; nil", false, nil
		}
		return marshalValue(val.Elem().Interface(), indent, hint)
	case reflect.Slice, reflect.Array:
		if val.Type().Elem().Kind() == reflect.Uint8 {
			bytes := base64.RawStdEncoding.EncodeToString(val.Bytes())
			wrappedBytes := ""
			for i := 0; i < len(bytes); i += 80 {
				if i+80 < len(bytes) {
					wrappedBytes += bytes[i:i+80] + "\n"
				} else {
					wrappedBytes += bytes[i:]
				}
			}
			return quoteValue(string(wrappedBytes), indent+"  ", hint), true, nil
		}
		fallthrough
	case reflect.Map, reflect.Struct:
		section, err := marshalSection(v, indent+"  ")
		if err != nil {
			return "", false, err
		}
		return "\n" + section, false, nil
	case reflect.String:
		return quoteValue(val.String(), indent+"  ", hint), true, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64, reflect.Complex64, reflect.Complex128, reflect.Bool:
		return fmt.Sprint(v), true, nil
	default:
		return "", false, fmt.Errorf("unsupported type: %s", val.Type())
	}
}

func marshalSection(v any, indent string) (string, error) {
	val := reflect.ValueOf(v)
	switch val.Kind() {
	case reflect.Pointer, reflect.Interface:
		return marshalSection(val.Elem().Interface(), indent)
	case reflect.Struct:
		strs := []string{}
		for i := range val.Type().NumField() {
			field := val.Type().Field(i)
			if !field.IsExported() {
				continue
			}
			tag, ok := field.Tag.Lookup("conl")
			if !ok {
				tag, _ = field.Tag.Lookup("json")
			}
			if tag == "-" {
				continue
			}
			name, options, found := strings.Cut(tag, ",")
			if !found {
				name = tag
			}
			if name == "" {
				name = field.Name
			}
			fv := val.Field(i)
			if strings.Contains(options, "omitempty") {
				if !fv.IsValid() || fv.IsZero() {
					continue
				}
			}
			hint := ""
			if _, tag, ok := strings.Cut(options, "hint="); ok {
				hint, _, _ = strings.Cut(tag, ",")
			}
			v, eq, err := marshalValue(fv.Interface(), indent, hint)
			if err != nil {
				return "", err
			}
			if eq {
				strs = append(strs, quoteString(name)+" = "+v)
			} else {
				strs = append(strs, quoteString(name)+v)
			}
		}

		if len(strs) == 0 {
			return indent + "; empty", nil
		}
		return indent + strings.Join(strs, "\n"+indent), nil
	case reflect.Map:
		strs := []string{}
		for _, key := range val.MapKeys() {
			k, err := marshalKey(key.Interface())
			if err != nil {
				return "", err
			}
			v, eq, err := marshalValue(val.MapIndex(key).Interface(), indent, "")
			if err != nil {
				return "", err
			}
			if eq {
				strs = append(strs, k+" = "+v)
			} else {
				strs = append(strs, k+v)
			}
		}
		if len(strs) == 0 {
			return indent + "; empty", nil
		}
		slices.Sort(strs)
		return indent + strings.Join(strs, "\n"+indent), nil
	case reflect.Slice, reflect.Array:
		strs := []string{}
		for i := range val.Len() {
			v, _, err := marshalValue(val.Index(i).Interface(), indent, "")
			if err != nil {
				return "", err
			}
			strs = append(strs, v)
		}
		if len(strs) == 0 {
			return indent + "; empty", nil
		}
		return indent + "= " + strings.Join(strs, "\n"+indent+"= "), nil
	default:
		return "", fmt.Errorf("unsupported type: %s", val.Kind())
	}
}

// Marshal converts a go value to a CONL document.
//
// It returns an error if the value could not be marshaled (for example if it
// contains a channel or a func).
func Marshal(v any) ([]byte, error) {
	str, err := marshalSection(v, "")
	return []byte(str + "\n"), err
}

// Unmarshaler is implemented by types that want to customize their CONL
// parsing. The provided iterator can be re-used (for example, using [conl.UnmarshalCONL]).
// The provided tokens have been pre-processed as described by [Tokens], and additionally
// filtered such that it only contains [MapKey], [ListItem], [Scalar], [NoValue], [Indent] and [Outdent].
// The first token of the iterator is always [MapKey], [ListItem], [Scalar], or [NoValue].
type Unmarshaler interface {
	UnmarshalCONL(tokens iter.Seq[Token]) error
}

// Unmarshal updates the value v with the data from the CONL document.
// v should be a non-nil pointer to a struct, slice, map, interface, array.
//
// For struct fields, CONL will first look for the name in a `conl:"name"` tag,
// then in a `json:"name"` tag, and finally use the snake_case version of the field
// name or the field name itself.
//
// When unmarshalling into an interface, CONL maps will be unmarshalled into
// a map[string]any, lists will be unmarshalled into []any, and scalars will
// be unmarshalled to string.
//
// If the CONL document is invalid, or doesn't match the type of v, then an
// error will be returned.
func Unmarshal(data []byte, v any) error {
	return UnmarshalCONL(Tokens(data), v)
}

// UnmarshalCONL is the same as Unmarshal, but you can pass it an existing
// stream of tokens (for example implementations of [Unmarshaler] might want
// to use this).
func UnmarshalCONL(tok iter.Seq[Token], v any) error {
	value := reflect.ValueOf(v)
	if value.Kind() != reflect.Ptr || value.IsNil() {
		return fmt.Errorf("invalid target, must be a non-nil pointer")
	}

	iter, done := iter.Pull(tok)
	defer done()
	lastLine := 0
	tokenErr := error(nil)
	nextToken := func() Token {
		for {
			token, valid := iter()
			if token.Error != nil {
				tokenErr = fmt.Errorf("%v: %s", token.Lno, token.Error)
				valid = false
			}
			if !valid {
				return Token{Lno: lastLine, Kind: Outdent, Content: ""}
			}
			if token.Kind == Comment || token.Kind == MultilineHint {
				continue
			}
			if token.Kind == MultilineScalar {
				token.Kind = Scalar
			}
			lastLine = token.Lno
			return token
		}
	}
	err := unmarshalValue(nextToken, value.Elem())
	if tokenErr != nil {
		return tokenErr
	}
	if err != nil {
		return err
	}
	return nil
}

func peekToken(nextToken func() Token) (Token, func() Token) {
	t := nextToken()
	first := true
	return t, func() Token {
		if first {
			first = false
			return t
		}
		return nextToken()
	}
}

func tokenIter(nextToken func() Token) iter.Seq[Token] {
	token := nextToken()
	if token.Kind == Indent {
		token = nextToken()
	}
	tokens := []Token{token}
	if token.Kind == ListItem || token.Kind == MapKey {
		indentCount := 0
	loop:
		for {
			token = nextToken()
			switch token.Kind {
			case Indent:
				indentCount += 1
			case Outdent:
				if indentCount == 0 {
					break loop
				}
				indentCount -= 1
			}
			tokens = append(tokens, token)
		}
	}
	return slices.Values(tokens)
}

func unmarshalValue(nextToken func() Token, v reflect.Value) error {
	if !v.CanSet() {
		panic(fmt.Errorf("cannot set value of type: %v", v.Type()))
	}
	if cu, ok := v.Addr().Interface().(Unmarshaler); ok {
		if err := cu.UnmarshalCONL(tokenIter(nextToken)); err != nil {
			return err
		}
		return nil
	}

	if tu, ok := v.Addr().Interface().(encoding.TextUnmarshaler); ok {
		token, next := peekToken(nextToken)
		if token.Kind == Scalar {
			if err := tu.UnmarshalText([]byte(token.Content)); err != nil {
				return fmt.Errorf("%d: %w", token.Lno, err)
			}
			return nil
		}
		nextToken = next
	}

	switch v.Kind() {
	case reflect.Struct:
		return unmarshalStruct(nextToken, v)
	case reflect.Map:
		return unmarshalMap(nextToken, v)
	case reflect.Interface:
		return unmarshalInterface(nextToken, v)
	case reflect.Ptr:

		if _, ok := v.Interface().(Unmarshaler); !ok {
			t, next := peekToken(nextToken)
			if t.Kind == NoValue {
				return nil
			}
			nextToken = next
		}
		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
		}
		return unmarshalValue(nextToken, v.Elem())
	case reflect.Array:
		return unmarshalArray(nextToken, v)
	case reflect.Slice:
		return unmarshalSlice(nextToken, v)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64,
		reflect.Complex64, reflect.Complex128,
		reflect.Bool,
		reflect.String:
		token := nextToken()
		if token.Kind == Scalar {
			return unmarshalScalar(token.Lno, token.Content, v)
		}
		return fmt.Errorf("%d: expected value", token.Lno)
	}

	return fmt.Errorf("unsupported type: %v", v.Type())
}

func unmarshalStruct(nextToken func() Token, v reflect.Value) error {
	t := v.Type()
	fieldMap := make(map[string]reflect.Value)

	for i := 0; i < t.NumField(); i++ {
		field := v.Field(i)
		fieldType := t.Field(i)

		if fieldType.PkgPath != "" {
			continue
		}

		if tag, ok := fieldType.Tag.Lookup("conl"); ok {
			if tag == "-" {
				continue
			}
			name, _, _ := strings.Cut(tag, ",")
			fieldMap[name] = field
			continue
		}

		if tag, ok := fieldType.Tag.Lookup("json"); ok {
			if tag == "-" {
				continue
			}
			name, _, _ := strings.Cut(tag, ",")
			fieldMap[name] = field
			continue
		}

		fieldMap[fieldType.Name] = field
		fieldMap[toSnakeCase(fieldType.Name)] = field
	}

	for {
		token := nextToken()
		switch token.Kind {
		case Indent:
			continue
		case MapKey:
			field, ok := fieldMap[token.Content]
			if !ok {
				return fmt.Errorf("%d: unknown field %s", token.Lno, token.Content)
			}
			if err := unmarshalValue(nextToken, field); err != nil {
				return err
			}
		case Outdent, NoValue:
			return nil

		default:
			return fmt.Errorf("%d: unexpected %v, expected %v", token.Lno, token.Kind, v.Type())
		}
	}
}

func toSnakeCase(s string) string {
	var result strings.Builder
	for i, r := range s {
		if i > 0 && unicode.IsUpper(r) {
			result.WriteRune('_')
		}
		result.WriteRune(unicode.ToLower(r))
	}
	return result.String()
}

func unmarshalInterface(nextToken func() Token, v reflect.Value) error {
	for {
		token := nextToken()
		switch token.Kind {
		case Indent:
			continue
		case MapKey:
			m := reflect.ValueOf(make(map[string]any))
			v.Set(m)
			key := reflect.ValueOf(token.Content)
			value := reflect.New(m.Type().Elem()).Elem()
			if err := unmarshalValue(nextToken, value); err != nil {
				return err
			}
			m.SetMapIndex(key, value)
			return unmarshalMap(nextToken, m)
		case ListItem:
			s := reflect.ValueOf(&[]any{}).Elem()
			value := reflect.New(s.Type().Elem()).Elem()
			if err := unmarshalValue(nextToken, value); err != nil {
				return err
			}
			s.Set(reflect.Append(s, value))

			if err := unmarshalSlice(nextToken, s); err != nil {
				return err
			}
			v.Set(s)
			return nil
		case Scalar:
			v.Set(reflect.ValueOf(token.Content))
			return nil
		case Outdent, NoValue:
			return nil
		default:
			return fmt.Errorf("%d: unexpected %v", token.Lno, token.Kind)
		}
	}
}

func unmarshalMap(nextToken func() Token, v reflect.Value) error {
	keyType := v.Type().Key()
	valueType := v.Type().Elem()

	for {
		token := nextToken()
		switch token.Kind {
		case Indent:
			continue
		case MapKey:
			if v.IsNil() {
				v.Set(reflect.MakeMap(v.Type()))
			}
			key := reflect.New(keyType).Elem()
			tok := Token{Lno: token.Lno, Content: token.Content, Kind: Scalar, Error: nil}
			if err := unmarshalValue(func() Token { return tok }, key); err != nil {
				return err
			}
			value := reflect.New(valueType).Elem()
			if err := unmarshalValue(nextToken, value); err != nil {
				return err
			}
			v.SetMapIndex(key, value)
		case Outdent, NoValue:
			return nil

		default:
			return fmt.Errorf("%d: unexpected %s, expected %s", token.Lno, token.Kind, MapKey)
		}
	}
}

func unmarshalSlice(nextToken func() Token, v reflect.Value) error {
	elemType := v.Type().Elem()

	if elemType.Kind() == reflect.Uint8 {
		token := nextToken()
		if token.Kind == Scalar {
			r := strings.NewReplacer(" ", "", "\t", "", "\n", "")
			input := r.Replace(token.Content)
			output, err := base64.RawStdEncoding.DecodeString(input)
			if err != nil {
				return fmt.Errorf("%d: %w", token.Lno, err)
			}
			v.Set(reflect.ValueOf(output))
			return nil
		}
		return fmt.Errorf("%d: expected value", token.Lno)
	}

	for {
		token := nextToken()
		switch token.Kind {
		case Indent:
			continue
		case ListItem:
			elem := reflect.New(elemType).Elem()
			if err := unmarshalValue(nextToken, elem); err != nil {
				return err
			}
			v.Set(reflect.Append(v, elem))
		case Outdent, NoValue:
			return nil

		default:
			return fmt.Errorf("%d: unexpected %s, expected %s", token.Lno, token.Kind, ListItem)
		}
	}
}

func unmarshalArray(nextToken func() Token, v reflect.Value) error {
	elemType := v.Type().Elem()

	i := 0
	for {
		token := nextToken()
		switch token.Kind {
		case ListItem:
			elem := reflect.New(elemType).Elem()
			if err := unmarshalValue(nextToken, elem); err != nil {
				return err
			}
			if v.Len() <= i {
				return fmt.Errorf("%d: too many elements, limit %d", token.Lno, i)
			}
			v.Index(i).Set(elem)
			i += 1
		case Outdent, NoValue:
			return nil

		default:
			return fmt.Errorf("%d: unexpected %s, expected list", token.Lno, token.Kind)
		}
	}
}

func unmarshalScalar(lno int, s string, v reflect.Value) error {
	switch v.Kind() {
	case reflect.String:
		v.SetString(s)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		i, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return err
		}
		if v.OverflowInt(i) {
			return fmt.Errorf("%d: invalid %s: %v", lno, v.Type(), i)
		}
		v.SetInt(i)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		u, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return err
		}
		if v.OverflowUint(u) {
			return fmt.Errorf("%d: invalid %s: %v", lno, v.Type(), u)
		}
		v.SetUint(u)
	case reflect.Float32, reflect.Float64:
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return err
		}
		if v.OverflowFloat(f) {
			return fmt.Errorf("%d: invalid %s: %v", lno, v.Type(), f)
		}
		v.SetFloat(f)
	case reflect.Complex64, reflect.Complex128:
		c, err := strconv.ParseComplex(s, 128)
		if err != nil {
			return err
		}
		if v.OverflowComplex(c) {
			return fmt.Errorf("%d: invalid %s: %v", lno, v.Type(), c)
		}
		v.SetComplex(c)
	case reflect.Bool:
		b, err := strconv.ParseBool(s)
		if err != nil {
			return err
		}
		v.SetBool(b)
	default:
		return fmt.Errorf("%d: unsupported type %s", lno, v.Type())
	}
	return nil
}
