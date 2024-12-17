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

// Unmarshal updates the value v with the data from the CONL document.
// v should be a non-nil pointer to a struct, slice, map, interface, array.
// Unmarshal acts similarly to json.Unmarshal.
//
// For struct fields, CONL will first look for the name in a `conl:"name"` tag,
// then in a `json:"name"` tag, and finally use the snake_case version of the field
// name or the field name itself.
//
// When unmarshalling into an interface, CONL maps will be unmarshalled into
// a map[string]any, lists will be unmarshalled into []any, and scalars will
// be unmarshalled to string.
//
// If the CONL document is invalid, or doesn't match the type of `v`, then an
// error will be returned.
func Unmarshal(data []byte, v any) error {
	value := reflect.ValueOf(v)
	if value.Kind() != reflect.Ptr || value.IsNil() {
		return fmt.Errorf("invalid target, must be a non-nil pointer")
	}

	iter, done := iter.Pull2(Tokens(string(data)))
	defer done()
	lastLine := 0
	nextToken := func() (int, Token) {
		for {
			line, token, valid := iter()
			if !valid {
				return lastLine, Token{Kind: endOfFile, Content: ""}
			}
			if token.Kind == Comment || token.Kind == MultilineHint {
				continue
			}
			lastLine = line
			return line, token
		}
	}
	return unmarshalValue(nextToken, value.Elem())
}

func unmarshalValue(nextToken func() (int, Token), v reflect.Value) error {
	if !v.CanSet() {
		panic(fmt.Errorf("cannot set value of type: %v", v.Type()))
	}

	if tu, ok := v.Addr().Interface().(encoding.TextUnmarshaler); ok {
		lno, token := nextToken()
		if token.Kind != Value && token.Kind != MultilineValue {
			return fmt.Errorf("%d: expected value", lno)
		}
		if err := tu.UnmarshalText([]byte(token.Content)); err != nil {
			return fmt.Errorf("%d: %w", lno, err)
		}
		return nil
	}

	switch v.Kind() {
	case reflect.Struct:
		return unmarshalStruct(nextToken, v)
	case reflect.Map:
		return unmarshalMap(nextToken, v)
	case reflect.Interface:
		return unmarshalInterface(nextToken, v)
	case reflect.Ptr:
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
		lno, token := nextToken()
		if token.Kind == Value || token.Kind == MultilineValue {
			return setBasicValue(lno, token.Content, v)
		}
		return fmt.Errorf("%d: expected value", lno)
	}

	return fmt.Errorf("unsupported type: %v", v.Type())
}

func unmarshalStruct(nextToken func() (int, Token), v reflect.Value) error {
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
		lno, token := nextToken()
		switch token.Kind {
		case Indent:
			continue
		case MapKey:
			field, ok := fieldMap[token.Content]
			if !ok {
				return fmt.Errorf("%d: unknown field %s", lno, token.Content)
			}
			if err := unmarshalValue(nextToken, field); err != nil {
				return err
			}
		case Outdent, NoValue, endOfFile:
			return nil

		default:
			return fmt.Errorf("%d: unexpected %v, expected %v", lno, token.Kind, v.Type())
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

func unmarshalInterface(nextToken func() (int, Token), v reflect.Value) error {
	for {
		lno, token := nextToken()
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
		case Value, MultilineValue:
			v.Set(reflect.ValueOf(token.Content))
			return nil
		case Outdent, NoValue, endOfFile:
			return nil
		default:
			return fmt.Errorf("%d: unexpected %v", lno, token.Kind)
		}
	}
}

func unmarshalMap(nextToken func() (int, Token), v reflect.Value) error {
	keyType := v.Type().Key()
	valueType := v.Type().Elem()

	for {
		lno, token := nextToken()
		switch token.Kind {
		case Indent:
			continue
		case MapKey:
			if v.IsNil() {
				v.Set(reflect.MakeMap(v.Type()))
			}
			key := reflect.New(keyType).Elem()
			if err := setBasicValue(lno, token.Content, key); err != nil {
				return fmt.Errorf("%d: invalid key: %v", lno, err)
			}
			value := reflect.New(valueType).Elem()
			if err := unmarshalValue(nextToken, value); err != nil {
				return err
			}
			v.SetMapIndex(key, value)
		case Outdent, NoValue, endOfFile:
			return nil

		default:
			return fmt.Errorf("%d: unexpected %s, expected %s", lno, token.Kind, MapKey)
		}
	}
}

func unmarshalSlice(nextToken func() (int, Token), v reflect.Value) error {
	elemType := v.Type().Elem()

	if elemType.Kind() == reflect.Uint8 {
		lno, token := nextToken()
		if token.Kind == Value || token.Kind == MultilineValue {
			r := strings.NewReplacer(" ", "", "\t", "", "\n", "")
			input := r.Replace(token.Content)
			output, err := base64.RawStdEncoding.DecodeString(input)
			if err != nil {
				return fmt.Errorf("%d: %w", lno, err)
			}
			v.Set(reflect.ValueOf(output))
			return nil
		}
		return fmt.Errorf("%d: expected value", lno)
	}

	for {
		lno, token := nextToken()
		switch token.Kind {
		case Indent:
			continue
		case ListItem:
			elem := reflect.New(elemType).Elem()
			if err := unmarshalValue(nextToken, elem); err != nil {
				return err
			}
			v.Set(reflect.Append(v, elem))
		case Outdent, NoValue, endOfFile:
			return nil

		default:
			return fmt.Errorf("%d: unexpected %s, expected %s", lno, token.Kind, ListItem)
		}
	}
}

func unmarshalArray(nextToken func() (int, Token), v reflect.Value) error {
	elemType := v.Type().Elem()

	i := 0
	for {
		lno, token := nextToken()
		switch token.Kind {
		case ListItem:
			elem := reflect.New(elemType).Elem()
			if err := unmarshalValue(nextToken, elem); err != nil {
				return err
			}
			if v.Len() <= i {
				return fmt.Errorf("%d: too many elements, limit %d", lno, i)
			}
			v.Index(i).Set(elem)
			i += 1
		case Outdent, NoValue, endOfFile:
			return nil

		default:
			return fmt.Errorf("%d: unexpected %s, expected list", lno, token.Kind)
		}
	}
}

func setBasicValue(lno int, s string, v reflect.Value) error {
	if tu, ok := v.Addr().Interface().(encoding.TextUnmarshaler); ok {
		if err := tu.UnmarshalText([]byte(s)); err != nil {
			return fmt.Errorf("%d: %w", lno, err)
		}
	}
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
		return fmt.Errorf("%d: unsupported type %s", v.Type(), s)
	}
	return nil
}
