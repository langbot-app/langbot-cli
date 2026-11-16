package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/langbot-app/langbot-cli/internal/command"
	"github.com/langbot-app/langbot-cli/internal/deploy"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	if len(os.Args) == 2 && os.Args[1] == "_update-check" {
		_ = deploy.RefreshUpdateCache(ctx)
		stop()
		return
	}
	if len(os.Args) == 3 && os.Args[1] == "_native-run" {
		err := deploy.NativeServe(ctx, os.Args[2])
		stop()
		if err != nil {
			os.Exit(1)
		}
		return
	}
	code := command.Execute(ctx, os.Args[1:], command.Dependencies{
		In: os.Stdin, Out: os.Stdout, Err: os.Stderr,
		LookupEnv: os.LookupEnv,
		Version:   version, Commit: commit, BuildDate: buildDate,
	})
	stop()
	os.Exit(code)
}
