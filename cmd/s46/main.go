package main

import (
	"fmt"
	"os"
	"strings"

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
		message := err.Error()
		if strings.HasPrefix(message, "[s46]") || strings.HasPrefix(message, "[s46✈]") {
			fmt.Fprintln(os.Stderr, message)
		} else {
			configPath := ""
			if flag := root.PersistentFlags().Lookup("config"); flag != nil {
				configPath = flag.Value.String()
			}
			fmt.Fprintf(os.Stderr, "%s error: %v\n", cli.OutputPrefix(cli.ProcessEnv(), configPath), err)
		}
		os.Exit(1)
	}
}
