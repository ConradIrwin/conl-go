package schema_test

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/ConradIrwin/conl-go"
	"github.com/ConradIrwin/conl-go/schema"
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

	schemaSchema, err := schema.Parse(schemaBytes)
	if err != nil {
		t.Fatalf("couldn't parse schema: %v", err)
	}
	anySchema, err := schema.Parse(anyBytes)
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

var metaSchema *schema.Schema

func examples(t *testing.T, fileName string, run func(*testing.T, *schema.Schema, []byte)) {
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
	metaSchema, err := schema.Parse(schemaInput)
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

			schema, err := schema.Parse([]byte(example[0]))
			if err != nil {
				t.Fatalf("couldn't parse schema: %v", err)
			}

			run(t, schema, []byte(example[1]))
		})
	}

}

func TestSchema(t *testing.T) {
	examples(t, "testdata/example_schemas.conl", func(t *testing.T, schema *schema.Schema, input []byte) {
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
	examples(t, "testdata/suggested_values.conl", func(t *testing.T, schema *schema.Schema, input []byte) {
		result := schema.Validate(input)

		for token := range conl.Tokens(input) {
			if token.Kind == conl.Comment {
				if strings.HasPrefix(token.Content, ";") {
					continue
				}
				suggestions := result.SuggestedValues(token.Lno)
				actual := make([]string, len(suggestions))
				for i, suggestion := range suggestions {
					actual[i] = suggestion.Value
					if suggestion.Docs != "" {
						actual[i] += " \"" + suggestion.Docs + "\""
					}
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
		startKey, endKey, startValue, endValue, startComment := schema.SplitLine(input)
		if startKey != a || endKey != b || startValue != c || endValue != d || startComment != e {
			t.Errorf("%s: expected: %d, %d, %d, %d, %d, got: %d, %d, %d, %d, %d", input, a, b, c, d, e, startKey, endKey, startValue, endValue, startComment)
		}
	}
}

func TestLoad(t *testing.T) {
	if !schema.Validate([]byte{}, func(schema string) (*schema.Schema, error) {
		return nil, nil
	}).Valid() {
		t.Fatalf("empty document should validate")
	}

	if !schema.Validate([]byte{}, func(schema string) (*schema.Schema, error) {
		return nil, fmt.Errorf("wow")
	}).Valid() {
		t.Fatalf("empty document should validate")
	}

	if len(schema.Validate([]byte(`"`), nil).Errors()) != 1 {
		t.Fatalf("isolated quote should not validate")
	}

	errs := schema.Validate([]byte("a\n\""), func(schema string) (*schema.Schema, error) {
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
	examples(t, "testdata/suggested_keys.conl", func(t *testing.T, schema *schema.Schema, input []byte) {
		result := schema.Validate(input)

		for token := range conl.Tokens(input) {
			if token.Kind == conl.Comment {
				if strings.HasPrefix(token.Content, ";") {
					continue
				}
				suggestions := result.SuggestedKeys(token.Lno - 1)
				actual := make([]string, len(suggestions))
				for i, suggestion := range suggestions {
					actual[i] = suggestion.Value
					if suggestion.Docs != "" {
						actual[i] += " \"" + suggestion.Docs + "\""
					}
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

func TestDocsForKey(t *testing.T) {
	sch, err := schema.Parse([]byte(`
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

	docs := sch.Validate([]byte("a = hello")).DocsForKey(1)
	expected := "Hello!"
	if docs != expected {
		t.Fatalf("expected docs: %#v, got: %#v", expected, docs)
	}

	docs = sch.Validate([]byte("b = hello")).DocsForKey(1)
	expected = ""
	if docs != expected {
		t.Fatalf("expected docs: %#v, got: %#v", expected, docs)
	}
}

func TestDocs(t *testing.T) {
	sch, err := schema.Parse([]byte(`
root = <root>
definitions
  root
    keys
      a
        matches = <hello>
        docs = Key!
      b
        matches = ship
        docs = B!

  hello
    scalar
      matches = hello
      docs = Value!
`))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	result := sch.Validate([]byte("a = hello\nb = ship"))

	value := result.DocsForValue(1)
	expected := "Value!"
	if value != expected {
		t.Fatalf("expected docs: %#v, got: %#v", expected, value)
	}

	key := result.DocsForKey(1)
	expected = "Key!"
	if key != expected {
		t.Fatalf("expected docs: %#v, got: %#v", expected, key)
	}

	key = result.DocsForKey(2)
	expected = "B!"
	if key != expected {
		t.Fatalf("expected docs: %#v, got: %#v", expected, key)
	}
}
