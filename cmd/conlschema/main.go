package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/ConradIrwin/conl-go/schema"
)

func main() {
	schemaFile := flag.String("schema", "", "CONL schema file to validate against")
	flag.Parse()

	if *schemaFile == "" {
		fmt.Fprintln(os.Stderr, "Error: --schema flag is required")
		os.Exit(1)
	}

	// Read schema file
	schemaBytes, err := os.ReadFile(*schemaFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading schema file: %v\n", err)
		os.Exit(1)
	}

	schema, err := schema.Parse(schemaBytes)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing schema: %v\n", err)
		os.Exit(1)
	}

	// Read stdin
	inputBytes, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading stdin: %v\n", err)
		os.Exit(1)
	}

	statusCode := 0

	for _, err := range schema.Validate(inputBytes).Errors() {
		statusCode += 1
		fmt.Println(err.Error())
	}

	os.Exit(statusCode)
}
