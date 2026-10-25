package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"

	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/platform"
)

// computerStatus uses the stored Server pin and Computer credential. It never
// turns a maintenance status probe into a plaintext authenticated HTTP call.
func computerStatus(ctx context.Context, args []string, out, stderr io.Writer) error {
	f := flags("computer status", stderr)
	root := f.String("root", "", "Daemon data directory")
	path := f.String("credentials", "", "private Computer credentials file")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("computer status does not accept positional arguments")
	}
	if *path == "" {
		if *root == "" {
			return fmt.Errorf("--root or --credentials is required")
		}
		*path = filepath.Join(*root, "credentials.json")
	}
	var credentials computers.Credentials
	if err := platform.ReadJSON(*path, &credentials); err != nil {
		return err
	}
	status, err := computers.RemoteStatus(ctx, credentials)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(status)
}
