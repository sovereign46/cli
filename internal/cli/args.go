package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func exactArgs(expected string, count int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) == count {
			return nil
		}
		if len(args) < count {
			return fmt.Errorf("missing argument\nexpected: %s", expected)
		}
		return fmt.Errorf("too many arguments\nexpected: %s", expected)
	}
}

func maxArgs(expected string, count int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) <= count {
			return nil
		}
		return fmt.Errorf("too many arguments\nexpected: %s", expected)
	}
}

func minArgs(expected string, count int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) >= count {
			return nil
		}
		return fmt.Errorf("missing argument\nexpected: %s", expected)
	}
}

func anyFlagChanged(cmd *cobra.Command, names ...string) bool {
	for _, name := range names {
		if cmd.Flags().Changed(name) {
			return true
		}
	}
	return false
}
