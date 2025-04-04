package conl_test

import (
	"encoding/json"
	"fmt"
	"iter"
	"os"
	"strings"
	"testing"

	"github.com/ConradIrwin/conl-go"
)

func stringToJSON(input string) string {
	bytes, _ := json.Marshal(input)
	return string(bytes)
}

func toJSON(content []byte) (string, error) {
	var output strings.Builder
	next, stop := iter.Pull(conl.Tokens(content))
	defer stop()
	err := sectionToJSON(next, &output, "")
	if err != nil {
		return "", err
	}
	return output.String(), nil
}

func sectionToJSON(next func() (conl.Token, bool), output *strings.Builder, indent string) error {
	var sectType string
outer:
	for token, ok := next(); ok; token, ok = next() {
		if token.Error != nil {
			return fmt.Errorf("%d: %w", token.Lno, token.Error)
		}
		switch token.Kind {
		case conl.Comment, conl.MultilineHint:
			continue
		case conl.Indent:
			err := sectionToJSON(next, output, indent+"  ")
			if err != nil {
				return err
			}
		case conl.Outdent:
			break outer
		case conl.ListItem:
			if sectType == "" {
				output.WriteString("[")
				sectType = "list"
			} else if sectType == "list" {
				output.WriteString(",")
			} else {
				return fmt.Errorf("unexpected list item in map")
			}
		case conl.MapKey:
			if sectType == "" {
				output.WriteString("{")
				sectType = "map"
			} else if sectType == "map" {
				output.WriteString(",")
			} else {
				return fmt.Errorf("unexpected map key in list")
			}
			output.WriteString(stringToJSON(token.Content))
			output.WriteString(":")
		case conl.Scalar, conl.MultilineScalar:
			output.WriteString(stringToJSON(token.Content))
		case conl.NoValue:
			output.WriteString("null")
		default:
			panic(fmt.Errorf("unhandled token: %s", token.Kind))
		}
	}

	switch sectType {
	case "":
		output.WriteString("{}")
	case "list":
		output.WriteString("]")
	case "map":
		output.WriteString("}")
	}
	return nil
}

func TestEquivalence(t *testing.T) {
	examples, err := os.ReadFile("testdata/examples.txt")
	if err != nil {
		t.Fatalf("Failed to read examples file: %v", err)
	}

	examplesStr := strings.ReplaceAll(string(examples), "␉", "\t")
	examplesStr = strings.ReplaceAll(examplesStr, "␊", "\r")

	for _, example := range strings.Split(examplesStr, "\n===\n") {
		parts := strings.SplitN(example, "\n---\n", 2)
		if len(parts) != 2 {
			t.Fatalf("Invalid example format: %s", example)
		}
		input, expected := parts[0], strings.TrimSpace(parts[1])

		output, err := toJSON([]byte(input))
		if err != nil {
			t.Fatalf("Failed to parse: %v\nInput: %s", err, input)
		} else if output != expected {
			t.Fatalf("Mismatch:\nInput: %#v\nExpected: %#v\nGot: %#v", input, expected, output)
		}
	}
}

func TestErrors(t *testing.T) {
	examples, err := os.ReadFile("testdata/errors.txt")
	if err != nil {
		t.Fatalf("Failed to read errors file: %v", err)
	}

	examplesStr := strings.ReplaceAll(string(examples), "␉", "\t")
	examplesStr = strings.ReplaceAll(examplesStr, "␊", "\r")

	for _, example := range strings.Split(examplesStr, "\n===\n") {
		parts := strings.SplitN(example, "\n---\n", 2)
		if len(parts) != 2 {
			t.Fatalf("Invalid example format: %s", example)
		}
		input, expected := parts[0], strings.TrimSpace(parts[1])

		input = strings.ReplaceAll(input, "?", "\xff")
		expected = strings.ReplaceAll(expected, "␣", " ")

		output, err := toJSON([]byte(input))
		if err == nil {
			t.Errorf("Expected to be unable to parse: %s\nGot: %s", input, output)
		} else {
			errMsg := strings.ReplaceAll(err.Error(), "␣", " ")
			if errMsg != expected {
				t.Errorf("Error mismatch:\nInput: %s\nExpected: %#v\nGot: %#v", input, expected, errMsg)
			}
		}
	}
}
func FuzzParse(f *testing.F) {
	f.Fuzz(func(t *testing.T, input []byte) {
		output, err := toJSON(input)
		if err == nil {
			out := any(nil)
			if err := json.Unmarshal([]byte(output), &out); err != nil {
				t.Fatal(output, err)
			}
		}
	})
}
