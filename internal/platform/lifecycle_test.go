package platform

import (
	"context"
	"io"
	"os"
	"testing"
	"time"
)

func TestManagedInputRequestsShutdown(t *testing.T) {
	input, output := io.Pipe()
	defer input.Close()
	defer output.Close()
	ctx, cancel, err := ManagedContext(context.Background(), os.Getppid(), input)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	if _, err = output.Write([]byte("still running\n")); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() != nil {
		t.Fatal("data on the management pipe stopped the Server")
	}
	if err = output.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("closing the management pipe did not request shutdown")
	}
}

func TestManagedContextRejectsWrongParent(t *testing.T) {
	if _, _, err := ManagedContext(context.Background(), os.Getpid(), nil); err == nil {
		t.Fatal("accepted a management process other than the actual parent")
	}
}
