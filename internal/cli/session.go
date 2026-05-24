package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

func sessionCommand(runtime Runtime, opts *options) *cobra.Command {
	cmd := &cobra.Command{Use: "session", Short: "session subcommands"}
	var title string
	land := &cobra.Command{
		Use:   "land [session]",
		Short: "prepare review-ready landing metadata",
		Args:  maxArgs("s46 session land [session]", 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			if err := app.requireCloudFeature("session land"); err != nil {
				return err
			}
			service := app.sessionService()
			sessionID := ""
			if len(args) == 1 {
				sessionID = args[0]
			} else {
				latest, ok, err := service.LatestSession(cmd.Context())
				if err != nil {
					return err
				}
				if !ok {
					return fmt.Errorf("no sessions found; start a coding session first or pass a session id")
				}
				sessionID = latest.ID
			}
			result, err := service.Land(cmd.Context(), sessionID, title)
			if err != nil {
				return err
			}
			if ok, err := app.writeStructured(result); ok {
				return err
			}
			lines := []string{
				fmt.Sprintf("# %s", result.Title),
				fmt.Sprintf("# Branch:  %s", result.Branch),
				fmt.Sprintf("# Ran-on:  %s", strings.Join(result.RanOn, " → ")),
				fmt.Sprintf("# Harness: %s · Model: %s", result.Harness, result.Model),
				fmt.Sprintf("# Session: %s · Cost: %s", result.ID, result.Cost),
			}
			if result.Status != "" {
				lines = append(lines, fmt.Sprintf("# Status:  %s", result.Status))
			}
			if result.BlockedReason != "" {
				lines = append(lines, fmt.Sprintf("# Blocked: %s", result.BlockedReason))
			}
			lines = append(lines,
				"",
				"Review package:",
				fmt.Sprintf("- Summary: %s", result.Review.Summary),
				fmt.Sprintf("- Checklist: %s", strings.Join(result.Review.Checklist, "; ")),
				"- Suggested next commands:",
			)
			commands := result.Review.SuggestedCommands
			if len(commands) == 0 {
				commands = []string{"git diff", "git status"}
			}
			for _, command := range commands {
				lines = append(lines, "  "+command)
			}
			return app.renderer.Lines(lines...)
		},
	}
	land.Flags().StringVar(&title, "title", "", "review title")
	cmd.AddCommand(land)
	return cmd
}
