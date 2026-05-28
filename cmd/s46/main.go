package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"

	"github.com/sovereign46/cli/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ctx.Done()
		stop()
	}()

	runtime := cli.Runtime{
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		Env:    cli.ProcessEnv(),
	}
	code := run(ctx, runtime, os.Args[1:])
	stop()
	os.Exit(code)
}

func run(ctx context.Context, runtime cli.Runtime, args []string) int {
	root := cli.NewRootCommand(runtime)
	root.SetContext(ctx)
	root.SetArgs(args)

	if err := root.Execute(); err != nil {
		if errors.Is(err, context.Canceled) {
			return 130
		}
		if renderErr := cli.RenderExecutionError(root, runtime, err); renderErr != nil {
			return 1
		}
		return 1
	}
	return 0
}
