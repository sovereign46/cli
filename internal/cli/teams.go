package cli

import (
	"fmt"
	"sort"

	"github.com/spf13/cobra"

	"github.com/sovereign46/cli/internal/config"
	"github.com/sovereign46/cli/internal/harness"
	"github.com/sovereign46/cli/internal/output"
	"github.com/sovereign46/cli/internal/strs"
)

func teamsCommand(runtime Runtime, opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "teams",
		Short: "list and switch connected teams",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			return runTeamsList(app)
		},
	}
	cmd.AddCommand(teamsListCommand(runtime, opts))
	cmd.AddCommand(teamsUseCommand(runtime, opts))
	return cmd
}

func teamsListCommand(runtime Runtime, opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "list connected team configurations",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			return runTeamsList(app)
		},
	}
}

func teamsUseCommand(runtime Runtime, opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "use <team>",
		Short: "switch the active connected team",
		Args:  exactArgs("s46 teams use <team>", 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			return app.withLock(cmd.Context(), func() error {
				return runTeamsUse(app, args[0])
			})
		},
	}
}

type teamListEntry struct {
	Name     string `json:"name"`
	Active   bool   `json:"active"`
	Region   string `json:"region"`
	Harness  string `json:"harness"`
	Model    string `json:"model"`
	Endpoint string `json:"endpoint"`
}

func runTeamsList(app *app) error {
	cfg, err := app.config.LoadConfig()
	if err != nil {
		return err
	}
	entries := teamListEntries(cfg)
	if ok, err := app.writeStructured(map[string]any{"activeTeam": cfg.ActiveTeam, "teams": entries}); ok {
		return err
	}
	if len(entries) == 0 {
		return app.renderer.Lines("[s46] no connected teams", "[s46] next: s46 login", "[s46] then: s46 connect <team> --harness=<name>")
	}
	return app.renderer.Lines(renderTeamsList(entries)...)
}

func teamListEntries(cfg config.Config) []teamListEntry {
	names := make([]string, 0, len(cfg.Teams))
	for name := range cfg.Teams {
		names = append(names, name)
	}
	sort.Strings(names)
	entries := make([]teamListEntry, 0, len(names))
	for _, name := range names {
		team := cfg.Teams[name]
		entries = append(entries, teamListEntry{
			Name:     name,
			Active:   name == cfg.ActiveTeam,
			Region:   team.Region,
			Harness:  strs.FirstNonEmpty(team.DefaultHarness, harness.DefaultName),
			Model:    team.DefaultModel,
			Endpoint: team.Endpoint,
		})
	}
	return entries
}

func renderTeamsList(entries []teamListEntry) []string {
	rows := make([][]string, 0, len(entries)+1)
	rows = append(rows, []string{"ACTIVE", "TEAM", "REGION", "HARNESS", "MODEL", "ENDPOINT"})
	for _, entry := range entries {
		active := ""
		if entry.Active {
			active = "*"
		}
		rows = append(rows, []string{active, entry.Name, entry.Region, entry.Harness, entry.Model, entry.Endpoint})
	}
	lines := []string{"[s46] connected teams:"}
	lines = append(lines, output.Table(rows[0], rows[1:])...)
	return lines
}

func runTeamsUse(app *app, teamName string) error {
	cfg, err := app.config.LoadConfig()
	if err != nil {
		return err
	}
	if _, ok := cfg.Teams[teamName]; !ok {
		return fmt.Errorf("team %q is not connected; run `s46 connect %s` first", teamName, teamName)
	}
	cfg.ActiveTeam = teamName
	if err := app.config.SaveConfig(cfg); err != nil {
		return err
	}
	if ok, err := app.writeStructured(map[string]any{"activeTeam": teamName}); ok {
		return err
	}
	return app.renderer.Lines(fmt.Sprintf("[s46] active team: %s", teamName))
}
