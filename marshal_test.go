package conl_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ConradIrwin/conl-go"
)

func TestMarshal(t *testing.T) {
	str := "a"

	for _, test := range []struct {
		name string
		in   any
		out  string
	}{
		{
			name: "map",
			in: map[string]any{
				"a": 1,
				"b": 2,
			},
			out: "a = 1\nb = 2\n",
		},
		{
			name: "mixed",
			in: map[string]any{
				"a": []int{1, 2, 3},
				"b": "wow\nthere",
			},
			out: `
				a
				  = 1
				  = 2
				  = 3
				b = """
				  wow
				  there
			`,
		},
		{
			name: "iface",
			in: struct {
				A any
				B *string
			}{
				A: any("wow"),
				B: &str,
			},
			out: `
				A = wow
				B = a
			`,
		},
		{
			name: "struct",
			in: struct {
				A int  `conl:"a"`
				B bool `conl:"b,omitempty"`
				c string
				D []int `conl:"-"`
				E bool  `conl:",omitempty"`
				F []byte
				G struct {
					H string
				}
			}{
				A: 1,
				B: false,
				c: "hi",
				D: []int{1},
				E: true,
				F: []byte{1, 2, 3},
			},
			out: `
				a = 1
				E = true
				F = AQID
				G
				  H = ""
			`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			bytes, err := conl.Marshal(test.in)
			if err != nil {
				t.Fatalf("failed to marshal: %v", err)
			}
			out := strings.Replace(strings.Trim(strings.Replace(test.out, "\n\t\t\t\t", "\n", -1), "\n\t")+"\n", "\t", "    ", -1)
			if string(bytes) != out {
				t.Fatalf("expected\n%s\ngot\n%s", out, string(bytes))
			}
		})
	}

}

func TestUnmarshal(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		target   interface{}
		expected interface{}
		wantErr  bool
	}{
		{
			name: "basic string map",
			input: `
name = John
age = 30
`,
			target:   &map[string]string{},
			expected: map[string]string{"name": "John", "age": "30"},
		},
		{
			name: "nested map",
			input: `
user
    name = John
    age = 30
settings
    theme = dark
    debug = true
`,
			target: &map[string]map[string]string{},
			expected: map[string]map[string]string{
				"user":     {"name": "John", "age": "30"},
				"settings": {"theme": "dark", "debug": "true"},
			},
		},
		{
			name: "simple list",
			input: `
colors
    = red
    = green
    = blue
`,
			target: &struct {
				Colors []string
			}{},
			expected: struct {
				Colors []string
			}{
				Colors: []string{"red", "green", "blue"},
			},
		},
		{
			name: "mixed types struct",
			input: `
name = "John"
age = "30"
active = "true"
score = "95.5"
tags
    = "developer"
    = "golang"
`,
			target: &struct {
				Name   string
				Age    int
				Active bool
				Score  float64
				Tags   []string
			}{},
			expected: struct {
				Name   string
				Age    int
				Active bool
				Score  float64
				Tags   []string
			}{
				Name:   "John",
				Age:    30,
				Active: true,
				Score:  95.5,
				Tags:   []string{"developer", "golang"},
			},
		},
		{
			name: "multiline string",
			input: `
description = """
    This is a
    multiline
    description
`,
			target: &struct {
				Description string
			}{},
			expected: struct {
				Description string
			}{
				Description: "This is a\nmultiline\ndescription",
			},
		},
		{
			name: "invalid number",
			input: `
age = "not a number"
`,
			target: &struct {
				Age int
			}{},
			wantErr: true,
		},
		{
			name:    "nil pointer",
			input:   `test = "value"`,
			target:  nil,
			wantErr: true,
		},
		{
			name: "escaped strings",
			input: `
message = "Hello \"World\""
path = "C:\\Program Files"
`,
			target: &map[string]string{},
			expected: map[string]string{
				"message": `Hello "World"`,
				"path":    `C:\Program Files`,
			},
		},
		{
			name: "complex nested structure",
			input: `
users
    john
        name = "John Doe"
        age = "30"
        roles
            = "admin"
            = "user"
    jane
        name = "Jane Smith"
        age = "25"
        roles
            = "user"
`,
			target: &map[string]map[string]any{},
			expected: map[string]map[string]any{
				"users": {
					"john": map[string]any{
						"name": "John Doe",
						"age":  "30",
						"roles": []any{
							"admin",
							"user",
						},
					},
					"jane": map[string]any{
						"name": "Jane Smith",
						"age":  "25",
						"roles": []any{
							"user",
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := conl.Unmarshal([]byte(tt.input), tt.target)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			// For structs and maps, compare the actual value to the expected
			actual := reflect.ValueOf(tt.target).Elem().Interface()
			if !reflect.DeepEqual(actual, tt.expected) {
				t.Errorf("got %+v, want %+v", actual, tt.expected)
			}
		})
	}
}

type script struct {
	s string
}

func (s script) MarshalText() ([]byte, error) {
	return []byte(strings.TrimSpace(s.s)), nil
}

func (s *script) UnmarshalText(b []byte) error {
	s.s = string(b) + "\n"
	return nil
}

func TestTextMarshal(t *testing.T) {
	type Test struct {
		Time   time.Time `conl:"time"`
		Script script    `conl:"script,hint=bash"`
	}

	input := Test{
		Time:   time.Date(2024, time.November, 1, 16, 0, 0, 0, time.UTC),
		Script: script{s: "#!/bin/bash\necho hello\n"},
	}
	bytes, err := conl.Marshal(input)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
		return
	}

	expected := `time = 2024-11-01T16:00:00Z
script = """bash
  #!/bin/bash
  echo hello
`

	if string(bytes) != expected {
		t.Errorf("expected %#v, got %#v", expected, string(bytes))
	}

	output := Test{}
	if err := conl.Unmarshal(bytes, &output); err != nil {
		t.Errorf("unexpected error: %v", err)
		return
	}

	if !reflect.DeepEqual(input, output) {
		t.Errorf("got %+v, want %+v", output, input)
	}

	output = Test{}
	if err := conl.Unmarshal([]byte("time = 2024-11-01T16:00:00Z"), &output); err != nil {
		t.Errorf("unexpected error: %v", err)
		return
	}
	if !reflect.DeepEqual(Test{Time: time.Date(2024, time.November, 1, 16, 0, 0, 0, time.UTC)}, output) {
		t.Errorf("got %+v, want %+v", output, input)
	}

	output = Test{}
	if err := conl.Unmarshal([]byte("tyme = 2024-11-01T16:00:00Z"), &output); err == nil {
		t.Errorf("expected error for unknown key, got nil")
		return
	} else {
		if err.Error() != "1: unknown field tyme" {
			t.Errorf("expected error message 'unknown field: tyme', got %v", err)
		}
	}

}

func TestBytes(t *testing.T) {
	type Test struct {
		Secret []byte `conl:"secret"`
	}

	input := Test{
		Secret: []byte("secret data"),
	}
	bytes, err := conl.Marshal(input)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
		return
	}

	expected := "secret = c2VjcmV0IGRhdGE\n"
	if expected != string(bytes) {
		t.Errorf("expected %#v, got %#v", expected, string(bytes))
	}

	output := Test{}
	if err := conl.Unmarshal(bytes, &output); err != nil {
		t.Errorf("unexpected error: %v", err)
		return
	}

	if !reflect.DeepEqual(input, output) {
		t.Errorf("got %+v, want %+v", output, input)
	}

	input = Test{
		Secret: []byte("secret data, but this time, very, very, very, VERY, VERY long"),
	}
	bytes, err = conl.Marshal(input)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
		return
	}

	expected = `secret = """
  c2VjcmV0IGRhdGEsIGJ1dCB0aGlzIHRpbWUsIHZlcnksIHZlcnksIHZlcnksIFZFUlksIFZFUlkgbG9u
  Zw
`

	if expected != string(bytes) {
		t.Errorf("expected %#v, got %#v", expected, string(bytes))
	}

	output = Test{}
	if err := conl.Unmarshal(bytes, &output); err != nil {
		t.Errorf("unexpected error: %v", err)
		return
	}

	if !reflect.DeepEqual(input, output) {
		t.Errorf("got %+v, want %+v", output, input)
	}
}

func TestNoValue(t *testing.T) {
	type Test struct {
		List []any             `conl:"list"`
		Map  map[string]string `conl:"map"`
	}

	input := "list\nmap"
	output := Test{}

	if err := conl.Unmarshal([]byte(input), &output); err != nil {
		t.Errorf("unexpected error: %v", err)
		return
	}

	if output.List != nil || output.Map != nil {
		t.Errorf("expected empty list and map, got %+v", output)
	}

	type Test2 struct {
		String string `conl:"string"`
	}

	input = "string = ; nope"
	output2 := Test2{}

	if err := conl.Unmarshal([]byte(input), &output2); err == nil {
		t.Errorf("expected error, got nil")
		return
	} else if err.Error() != "1: expected value" {
		t.Logf("expected error: %v, got: %v", "1: expected value", err)
	}

	type Test3 struct {
		Bytes []byte `conl:"bytes"`
	}

	input = "bytes = ; nope"
	output3 := Test3{}

	if err := conl.Unmarshal([]byte(input), &output3); err == nil {
		t.Errorf("expected error, got nil")
		return
	} else if err.Error() != "1: expected value" {
		t.Logf("expected error: %v, got: %v", "1: expected value", err)
	}
}
