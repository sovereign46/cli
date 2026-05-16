package main

import (
	"fmt"
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
		fmt.Fprintf(os.Stderr, "s46: %v\n", err)
		os.Exit(1)
	}
}
