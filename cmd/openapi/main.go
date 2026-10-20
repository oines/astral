package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/oines/astral/internal/httpapi"
)

func main() {
	output := flag.String("output", "spec/openapi/astral.json", "generated OpenAPI path")
	flag.Parse()
	raw, err := json.MarshalIndent(httpapi.Contract().Document(), "", "  ")
	if err == nil {
		err = os.MkdirAll(filepath.Dir(*output), 0755)
	}
	if err == nil {
		err = os.WriteFile(*output, append(raw, '\n'), 0644)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
