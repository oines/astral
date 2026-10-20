package main

import (
	"context"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/oines/astral/internal/cli"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	args := os.Args[1:]
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		args = append([]string{"serve"}, args...)
	}
	os.Exit(cli.Run(ctx, args, os.Stdout, os.Stderr))
}
