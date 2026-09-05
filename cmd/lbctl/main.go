package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/langbot-app/langbot-cli/internal/command"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := command.Execute(ctx, os.Args[1:], command.Dependencies{
		In: os.Stdin, Out: os.Stdout, Err: os.Stderr,
		LookupEnv: os.LookupEnv,
		Version: version, Commit: commit, BuildDate: buildDate,
	})
	stop()
	os.Exit(code)
}
