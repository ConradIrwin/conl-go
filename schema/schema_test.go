package schema

import (
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ConradIrwin/conl-go"
)

func TestSchemaSelf(t *testing.T) {
	schemaBytes, err := os.ReadFile("testdata/schema.schema.conl")
	if err != nil {
		t.Fatalf("Failed to read schema.conl: %v", err)
	}

	anyBytes, err := os.ReadFile("testdata/any.schema.conl")
	if err != nil {
		t.Fatalf("Failed to read schema.conl: %v", err)
	}

	schemaSchema, err := Parse(schemaBytes)
	if err != nil {
		t.Fatalf("couldn't parse schema: %v", err)
	}
	anySchema, err := Parse(anyBytes)
	if err != nil {
		t.Fatalf("couldn't parse schema: %v", err)
	}

	errs := anySchema.Validate(anyBytes).Errors()
	if errs != nil {
		for _, err := range errs {
			t.Log(err.Error())
		}
		t.Fatal("any did not match any")
	}
	errs = schemaSchema.Validate(anyBytes).Errors()
	t.Log("--------------------------------------")
	t.Log(errs)
	if errs != nil {
		for _, err := range errs {
			t.Log(err.Error())
		}
		t.Fatal("schema did not match any")
	}
	errs = anySchema.Validate(schemaBytes).Errors()
	if errs != nil {
		for _, err := range errs {
			t.Log(err.Error())
		}
		t.Fatal("any did not match schema")
	}
	errs = schemaSchema.Validate(schemaBytes).Errors()
	if errs != nil {
		for _, err := range errs {
			t.Log(err.Error())
		}
		t.Fatal("schema did not match schema")
	}
}

var metaSchema *Schema

func examples(t *testing.T, fileName string, run func(*testing.T, *Schema, []byte)) {
	t.Helper()

	examples := map[string][]string{}
	input, err := os.ReadFile(fileName)
	if err != nil {
		t.Fatalf("Failed to read %s: %v", fileName, err)
	}
	if err := conl.Unmarshal(input, &examples); err != nil {
		t.Fatalf("Failed to parse %s: %v", fileName, err)
	}

	schemaInput, err := os.ReadFile("testdata/schema.schema.conl")
	if err != nil {
		t.Fatalf("Failed to read schema.schema.conl: %v", err)
	}
	metaSchema, err := Parse(schemaInput)
	if err != nil {
		t.Fatalf("couldn't parse schema.schema.conl: %v", err)
	}

	for name, example := range examples {
		t.Run(name, func(t *testing.T) {
			errs := metaSchema.Validate([]byte(example[0])).Errors()
			if errs != nil {
				for _, err := range errs {
					t.Log(err.Error())
				}
				t.Fatal("schema validation failed")
			}

			schema, err := Parse([]byte(example[0]))
			if err != nil {
				t.Fatalf("couldn't parse schema: %v", err)
			}

			run(t, schema, []byte(example[1]))
		})
	}

}

func TestSchema(t *testing.T) {
	examples(t, "testdata/example_schemas.conl", func(t *testing.T, schema *Schema, input []byte) {
		expected := []string{}
		for token := range conl.Tokens(input) {
			if token.Kind == conl.Comment {
				if strings.HasPrefix(token.Content, ";") {
					continue
				}
				for _, msg := range strings.Split(token.Content, ";") {
					expected = append(expected, fmt.Sprintf("%d: %s", token.Lno, strings.Trim(msg, " ")))
				}
			}
		}

		errors := schema.Validate(input).Errors()
		actual := []string{}
		for _, err := range errors {
			line := strings.Split(string(input), "\n")[err.Lno()-1]
			start, end := err.RuneRange(line)
			actual = append(actual, fmt.Sprintf("%v: %v-%v %v", err.Lno(), start, end, err.Msg()))
		}
		if !slices.Equal(expected, actual) {
			t.Logf("expected:")
			for _, err := range expected {
				t.Log(err)
			}
			t.Logf("actual:")
			for _, err := range actual {
				t.Log(err)
			}
			t.FailNow()
		}

	})
}

func TestSuggestedValues(t *testing.T) {
	examples(t, "testdata/suggested_values.conl", func(t *testing.T, schema *Schema, input []byte) {
		result := schema.Validate(input)

		for token := range conl.Tokens(input) {
			if token.Kind == conl.Comment {
				if strings.HasPrefix(token.Content, ";") {
					continue
				}
				suggestions, _ := result.SuggestedValues(token.Lno)
				actual := make([]string, len(suggestions))
				for i, suggestion := range suggestions {
					actual[i] = suggestion.Value
				}

				expected := strings.Split(strings.TrimSpace(token.Content), ",")
				if strings.TrimSpace(token.Content) == "" {
					expected = []string{}
				}

				if !slices.Equal(actual, expected) {
					t.Fatalf("%v: expected %v, got %v", token.Lno, expected, actual)
				}
			}
		}
	})
}

func TestSuggestedValuesDocs(t *testing.T) {
	sch, err := Parse([]byte(`
root = <root>
definitions
  root
    keys
      a = <test>

  test
    one of
      =
        matches = a
        docs = Hello!
`))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	suggestions, _ := sch.Validate([]byte("a = ")).SuggestedValues(1)
	expected := []*Suggestion{
		{Value: "a", Docs: "Hello!"},
	}
	if !reflect.DeepEqual(suggestions, expected) {
		t.Fatalf("expected suggestions: %#v, got: %#v", expected, suggestions)
	}
}

func TestSplitLine(t *testing.T) {
	processString := func(input string) (string, int, int, int, int, int) {
		var result strings.Builder
		var indexes []int
		currentIndex := 0

		for _, char := range input {
			if char == '|' {
				indexes = append(indexes, currentIndex)
			} else {
				result.WriteRune(char)
				currentIndex++
			}
		}
		for len(indexes) < 5 {
			indexes = append(indexes, indexes[len(indexes)-1])
		}

		return result.String(), indexes[0], indexes[1], indexes[2], indexes[3], indexes[4]
	}

	for _, string := range []string{
		"|a| = |a| |;a",
		`|"a = a"| = |"b =|`,
		`|"a = a = "b| =|||;`,
		`  |a| = |b|`,
		"|=|",
	} {
		input, a, b, c, d, e := processString(string)
		startKey, endKey, startValue, endValue, startComment := splitLine(input)
		if startKey != a || endKey != b || startValue != c || endValue != d || startComment != e {
			t.Errorf("%s: expected: %d, %d, %d, %d, %d, got: %d, %d, %d, %d, %d", input, a, b, c, d, e, startKey, endKey, startValue, endValue, startComment)
		}
	}
}

func TestLoad(t *testing.T) {
	if !Validate([]byte{}, func(schema string) (*Schema, error) {
		return nil, nil
	}).Valid() {
		t.Fatalf("empty document should validate")
	}

	if !Validate([]byte{}, func(schema string) (*Schema, error) {
		return nil, fmt.Errorf("wow")
	}).Valid() {
		t.Fatalf("empty document should validate")
	}

	if len(Validate([]byte(`"`), nil).Errors()) != 1 {
		t.Fatalf("isolated quote should not validate")
	}

	errs := Validate([]byte("a\n\""), func(schema string) (*Schema, error) {
		return nil, fmt.Errorf("failed to load schema")
	}).Errors()
	if len(errs) != 2 {
		for _, err := range errs {
			t.Log(err.Error())
		}
		t.Fatalf("schema errors should be reported in addition to content errors")
	}
	if errs[0].Error() != "1: failed to load schema" {
		t.Fatalf("got %#v, not schema error", errs[0].Error())
	}
	if errs[1].Error() != "2: unclosed quotes" {
		t.Fatalf("got %#v, not quote error", errs[1].Error())
	}
}

func TestSuggestedKeys(t *testing.T) {
	sch, err := Parse([]byte(`
root = <root>
definitions
  root
    keys
      a = .*
      b = .*
	`))
	if err != nil {
		t.Fatalf("failed to parse schema: %v", err)
	}

	suggestions := sch.Validate([]byte("")).SuggestedKeys(0)
	if len(suggestions) != 2 || suggestions[0].Value != "a" || suggestions[1].Value != "b" {
		t.Fatalf("expected suggestions: %v, got: %v", []string{"a", "b"}, suggestions)
	}

	suggestions = sch.Validate([]byte("a = 1\n")).SuggestedKeys(0)
	if len(suggestions) != 1 || suggestions[0].Value != "b" {
		t.Fatalf("expected suggestions: %v, got: %v", []string{"a", "b"}, suggestions)
	}

	sch, err = Parse([]byte(`

root = <root>
definitions
  root
    keys
      a = <nested>

  nested
    keys
      b = .*
      c = .*
	`))

	if err != nil {
		t.Fatalf("failed to parse schema: %v", err)
	}

	suggestions = sch.Validate([]byte("a\n  ")).SuggestedKeys(1)
	if len(suggestions) != 2 || suggestions[0].Value != "b" || suggestions[1].Value != "c" {
		t.Fatalf("expected suggestions: %v, got: %v", []string{"b", "c"}, suggestions)
	}

	sch, err = Parse([]byte(`
root = <root>
definitions
  root
    keys
      a = <nested>

  nested
    one of
      = <b map>
      = <c map>

  b map
    required keys
      b = .*

  c map
    required keys
      c = .*
	`))

	if err != nil {
		t.Fatalf("failed to parse schema: %v", err)
	}

	suggestions = sch.Validate([]byte("a\n  ")).SuggestedKeys(1)
	if len(suggestions) != 2 || suggestions[0].Value != "b" || suggestions[1].Value != "c" {
		t.Fatalf("expected suggestions: %v, got: %v", []string{"b", "c"}, suggestions)
	}

	sch, err = Parse([]byte(`
root = <root>
definitions
  root
    keys
      a = <nested>

  nested
    keys
      b = <wow>

  wow
    required keys
      d = .*
    keys
      e = .*
	`))

	if err != nil {
		t.Fatalf("failed to parse schema: %v", err)
	}

	suggestions = sch.Validate([]byte("a\n  b\n")).SuggestedKeys(2)
	if len(suggestions) != 2 || suggestions[0].Value != "d" || suggestions[1].Value != "e" {
		t.Fatalf("expected suggestions: %v, got: %v", []string{"d", "e"}, suggestions)
	}

	sch, err = Parse([]byte(`
root = <root>
definitions
  root
    keys
      a
        matches = hello
        docs = Hello!
`))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	suggestions = sch.Validate([]byte("")).SuggestedKeys(0)
	expected := []*Suggestion{
		{Value: "a", Docs: "Hello!"},
	}
	if !reflect.DeepEqual(suggestions, expected) {
		t.Fatalf("expected suggestions: %#v, got: %#v", expected, suggestions)
	}
}
