package main

import (
	"os"

	"github.com/sovereign46/s46-cli/internal/cli"
)

func main() {
	root := cli.NewRootCommand(cli.Runtime{
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		Env:    cli.ProcessEnv(),
	})

	if err := root.Execute(); err != nil {
		_ = cli.RenderExecutionError(root, cli.Runtime{Stdout: os.Stdout, Stderr: os.Stderr, Env: cli.ProcessEnv()}, err)
		os.Exit(1)
	}
}
