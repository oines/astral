//go:build !linux

package serverupdate

import (
	"context"
	"errors"
	"io"
)

func ownedFile(string, int) bool { return false }
func installCommand(context.Context, string, []string, io.Writer, io.Writer) error {
	return errors.New("linux user service required")
}
