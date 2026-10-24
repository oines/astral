package cli

import (
	"encoding/json"
	"fmt"
	"io"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/persistence"
)

func version(args []string, out io.Writer) error {
	var mode string
	for _, arg := range args {
		switch arg {
		case "--json", "--data-formats":
			if mode == arg {
				return fmt.Errorf("astral version: %s may only be specified once", arg)
			}
			if mode != "" {
				return fmt.Errorf("astral version: --json and --data-formats cannot be combined")
			}
			mode = arg
		default:
			return fmt.Errorf("astral version: unknown argument %q; usage: astral version [--json | --data-formats]", arg)
		}
	}
	switch mode {
	case "--json":
		metadata := struct {
			Version     string `json:"version"`
			DataFormats struct {
				Host int `json:"host"`
				Core int `json:"core"`
			} `json:"data_formats"`
		}{Version: Version}
		metadata.DataFormats.Host = persistence.FormatVersion
		metadata.DataFormats.Core = core.FormatVersion
		return json.NewEncoder(out).Encode(metadata)
	case "--data-formats":
		_, err := fmt.Fprintf(out, "%d %d\n", persistence.FormatVersion, core.FormatVersion)
		return err
	default:
		_, err := fmt.Fprintln(out, Version)
		return err
	}
}
