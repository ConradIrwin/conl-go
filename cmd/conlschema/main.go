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
	flag.StringVar(schemaFile, "s", "", "(alias for --schema)")
	flag.Parse()

	if *schemaFile == "" || len(flag.Args()) > 1 {
		fmt.Fprintln(os.Stderr, "Usage: conlschema --schema <schema> [input]")
		os.Exit(1)
	}

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

	var inputBytes []byte
	if len(flag.Args()) == 1 {
		inputBytes, err = os.ReadFile(flag.Arg(0))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading %v: %v\n", flag.Arg(0), err)
			os.Exit(1)
		}
	} else {
		inputBytes, err = io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading stdin: %v\n", err)
			os.Exit(1)
		}
	}

	statusCode := 0

	for _, err := range schema.Validate(inputBytes).Errors() {
		statusCode += 1
		fmt.Println(err.Error())
	}

	os.Exit(statusCode)
}
