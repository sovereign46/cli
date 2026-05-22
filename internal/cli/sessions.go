package cli

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/sovereign46/cli/internal/output"
	sessioncmd "github.com/sovereign46/cli/internal/session"
	"github.com/sovereign46/cli/internal/strs"
)

func sessionsCommand(runtime Runtime, opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "sessions",
		Short: "list local and remote sessions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			service := app.sessionService()
			sessions, err := service.ListEntries(cmd.Context())
			if err != nil {
				return err
			}
			if ok, err := app.writeStructured(map[string]any{"sessions": sessions}); ok {
				return err
			}
			if len(sessions) == 0 {
				return app.renderer.Lines("[s46] no sessions found", "[s46] next: start a coding session, then run `s46 sessions` again")
			}
			sessionIDs := displaySessionIDs(sessions)
			rows := make([][]string, 0, len(sessions))
			for i, session := range sessions {
				rows = append(rows, []string{sessionIDs[i], session.State, session.Harness, displayTableCell(session.Model), strs.FirstNonEmpty(session.Age, "0m"), displayTableCell(session.Spent), displaySessionTask(session.Task)})
			}
			return app.renderer.Lines(output.Table([]string{"ID", "STATE", "HARNESS", "MODEL", "AGE", "SPENT", "TASK"}, rows)...)
		},
	}
}

func displaySessionIDs(sessions []sessioncmd.ListedSession) []string {
	trimmedIDs := make([]string, len(sessions))
	for i, session := range sessions {
		trimmedIDs[i] = strings.TrimSpace(session.ID)
	}
	ids := make([]string, len(sessions))
	for i, id := range trimmedIDs {
		ids[i] = uniqueSessionIDPrefix(id, trimmedIDs)
	}
	return ids
}

func uniqueSessionIDPrefix(id string, sessionIDs []string) string {
	if !looksLikeUUID(id) {
		return id
	}
	for length := 8; length < len(id); length++ {
		prefix := id[:length]
		if sessionIDPrefixUnique(id, prefix, sessionIDs) {
			return prefix
		}
	}
	return id
}

func sessionIDPrefixUnique(id string, prefix string, sessionIDs []string) bool {
	for _, other := range sessionIDs {
		if other != id && strings.HasPrefix(other, prefix) {
			return false
		}
	}
	return true
}

func displaySessionID(id string) string {
	id = strings.TrimSpace(id)
	if looksLikeUUID(id) {
		return id[:8]
	}
	return id
}

func looksLikeUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for _, idx := range []int{8, 13, 18, 23} {
		if value[idx] != '-' {
			return false
		}
	}
	for _, r := range value {
		if r == '-' {
			continue
		}
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return false
		}
	}
	return true
}

func displayTableCell(value string) string {
	if strings.TrimSpace(value) == "" {
		return "—"
	}
	return value
}

func displaySessionTask(task string) string {
	return displayTableCell(compactSessionTask(task, 72))
}

func compactSessionTask(task string, limit int) string {
	task = strings.Join(strings.Fields(task), " ")
	if task == "" || limit <= 0 {
		return task
	}
	runes := []rune(task)
	if len(runes) <= limit {
		return task
	}
	if limit == 1 {
		return "…"
	}
	return string(runes[:limit-1]) + "…"
}
