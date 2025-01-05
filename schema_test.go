package conl_test

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
	for lno, token := range conl.Tokens(input) {
		if token.Kind == conl.Comment {
			if strings.HasPrefix(token.Content, ";") {
				continue
			}
			for _, msg := range strings.Split(token.Content, ";") {
				output = append(output, fmt.Sprintf("%d: %s", lno, strings.Trim(msg, " ")))
			}
		}
	}
	return output
}

func TestSchema(t *testing.T) {

	examples, err := os.ReadFile("testdata/schemas.conl")
	if err != nil {
		t.Fatalf("Failed to read examples file: %v", err)
	}

	examplesStr := strings.ReplaceAll(string(examples), "␉", "\t")
	examplesStr = strings.ReplaceAll(examplesStr, "␊", "\r")

	for _, example := range strings.Split(examplesStr, "\n===\n") {
		parts := strings.SplitN(example, "\n---\n", 2)
		comment, _, _ := strings.Cut(parts[0], "\n")

		t.Run(strings.Trim(comment, "; "), func(t *testing.T) {
			schema, err := conl.ParseSchema([]byte(parts[0]))
			if err != nil {
				t.Fatalf("couldn't parse schema: %v", err)
			}
			expected := collectErrors(parts[1])
			errors := schema.Validate(parts[1])
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
