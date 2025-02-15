package schema

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/ConradIrwin/conl-go"
)

func collectErrors(input string) []string {
	output := []string{}
	for token := range conl.Tokens([]byte(input)) {
		if token.Kind == conl.Comment {
			if strings.HasPrefix(token.Content, ";") {
				continue
			}
			for _, msg := range strings.Split(token.Content, ";") {
				output = append(output, fmt.Sprintf("%d: %s", token.Lno, strings.Trim(msg, " ")))
			}
		}
	}
	return output
}

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

	errs := anySchema.Validate(anyBytes)
	if errs != nil {
		for _, err := range errs {
			t.Log(err.Error())
		}
		t.Fatal("any did not match any")
	}
	errs = schemaSchema.Validate(anyBytes)
	if errs != nil {
		for _, err := range errs {
			t.Log(err.Error())
		}
		t.Fatal("schema did not match any")
	}
	errs = anySchema.Validate(schemaBytes)
	if errs != nil {
		for _, err := range errs {
			t.Log(err.Error())
		}
		t.Fatal("any did not match schema")
	}
	errs = schemaSchema.Validate(schemaBytes)
	if errs != nil {
		for _, err := range errs {
			t.Log(err.Error())
		}
		t.Fatal("schema did not match schema")
	}
}

func TestSchema(t *testing.T) {
	examples, err := os.ReadFile("testdata/example_schemas.conl")
	if err != nil {
		t.Fatalf("Failed to read examples file: %v", err)
	}

	examplesStr := strings.ReplaceAll(string(examples), "␉", "\t")
	examplesStr = strings.ReplaceAll(examplesStr, "␊", "\r")

	input, err := os.ReadFile("testdata/schema.schema.conl")
	if err != nil {
		t.Fatalf("Failed to read schema.conl: %v", err)
	}

	metaSchema, err := Parse(input)
	if err != nil {
		t.Fatalf("couldn't parse schema: %v", err)
	}

	for _, example := range strings.Split(examplesStr, "\n===\n") {
		parts := strings.SplitN(example, "\n---\n", 2)
		comment, _, _ := strings.Cut(parts[0], "\n")

		t.Run(strings.Trim(comment, "; "), func(t *testing.T) {
			errs := metaSchema.Validate([]byte(parts[0]))
			if errs != nil {
				for _, err := range errs {
					t.Log(err.Error())
				}
				t.Fatal("schema validation failed")
			}

			schema, err := Parse([]byte(parts[0]))
			if err != nil {
				t.Fatalf("couldn't parse schema: %v", err)
			}
			expected := collectErrors(parts[1])
			errors := schema.Validate([]byte(parts[1]))
			actual := []string{}
			for _, err := range errors {
				line := strings.Split(parts[1], "\n")[err.Lno()-1]
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
}

func processString(input string) (string, int, int, int, int, int) {
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

func TestSplitLine(t *testing.T) {
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
	if Validate([]byte{}, func(schema string) (*Schema, error) {
		return nil, nil
	}) != nil {
		t.Fatalf("empty document should validate")
	}

	if Validate([]byte{}, func(schema string) (*Schema, error) {
		return nil, fmt.Errorf("wow")
	}) != nil {
		t.Fatalf("empty document should validate")
	}

	if len(Validate([]byte(`"`), nil)) != 1 {
		t.Fatalf("isolated quote should not validate")
	}

	errs := Validate([]byte("a\n\""), func(schema string) (*Schema, error) {
		return nil, fmt.Errorf("failed to load schema")
	})
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
