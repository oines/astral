package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/oines/astral/internal/serverupdate"
)

func serverMaintenance(ctx context.Context, args []string, out, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "update" {
		return fmt.Errorf("use Server Settings to request an update; this helper is managed by its current-user service")
	}
	f := flags("server update", stderr)
	root := f.String("root", "", "official Server installation root")
	operation := f.String("operation", "", "existing managed update operation")
	managed := f.Bool("managed-helper", false, "run the already prepared update as an independent user task")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if !*managed || *root == "" || *operation == "" || f.NArg() != 0 {
		return fmt.Errorf("server update requires --root, --operation and --managed-helper; request the update in Server Settings")
	}
	return serverupdate.Run(ctx, *root, *operation, out, stderr)
}
