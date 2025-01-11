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
	input, err := os.ReadFile("testdata/schema.conl")
	if err != nil {
		t.Fatalf("Failed to read schema.conl: %v", err)
	}

	schema, err := schema.Parse(input)
	if err != nil {
		t.Fatalf("couldn't parse schema: %v", err)
	}
	errs := schema.Validate(input)
	if errs != nil {
		for _, err := range errs {
			t.Log(err.Error())
		}
		t.Fatal("schema validation failed")
	}
}

func TestSchema(t *testing.T) {
	examples, err := os.ReadFile("testdata/example_schemas.conl")
	if err != nil {
		t.Fatalf("Failed to read examples file: %v", err)
	}

	examplesStr := strings.ReplaceAll(string(examples), "␉", "\t")
	examplesStr = strings.ReplaceAll(examplesStr, "␊", "\r")

	input, err := os.ReadFile("testdata/schema.conl")
	if err != nil {
		t.Fatalf("Failed to read schema.conl: %v", err)
	}

	metaSchema, err := schema.Parse(input)
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

			schema, err := schema.Parse([]byte(parts[0]))
			if err != nil {
				t.Fatalf("couldn't parse schema: %v", err)
			}
			expected := collectErrors(parts[1])
			errors := schema.Validate([]byte(parts[1]))
			actual := []string{}
			for _, err := range errors {
				actual = append(actual, err.Error())
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
