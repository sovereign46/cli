package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	sessioncmd "github.com/sovereign46/cli/internal/session"
)

func shareCommand(runtime Runtime, opts *options) *cobra.Command {
	var ttl string
	cmd := &cobra.Command{
		Use:   "share [session]",
		Short: "share a session as an encrypted static page",
		Args:  maxArgs("s46 share [session]", 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			sessionID := ""
			if len(args) == 1 {
				sessionID = args[0]
			}
			return runShare(cmd.Context(), app, sessionID, ttl)
		},
	}
	cmd.Flags().StringVar(&ttl, "ttl", "30d", "share expiration: 1d, 7d, 30d, 365d, never")
	cmd.AddCommand(&cobra.Command{
		Use:   "revoke <session-or-share-id>",
		Short: "delete a previously created encrypted share",
		Args:  exactArgs("s46 share revoke <session-or-share-id>", 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			return runShareRevoke(cmd.Context(), app, args[0])
		},
	})
	return cmd
}

func runShare(ctx context.Context, app *app, sessionID string, ttl string) error {
	service := app.sessionService()
	var inferred *sessioncmd.ListedSession
	if strings.TrimSpace(sessionID) == "" {
		latest, ok, err := service.LatestSession(ctx)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("no sessions found; start a coding session, run `s46 sessions`, or pass a session id")
		}
		sessionID = latest.ID
		inferred = &latest
	}
	if err := app.requireCloudFeature("share"); err != nil {
		if inferred != nil {
			return fmt.Errorf("latest session is %s; %w", inferred.ID, err)
		}
		return err
	}
	var result sessioncmd.ShareResult
	if err := app.withLock(ctx, func() error {
		var err error
		result, err = service.Share(ctx, sessionID, ttl)
		return err
	}); err != nil {
		return err
	}
	if ok, err := app.writeStructured(result); ok {
		return err
	}
	lines := inferredShareLines(inferred)
	verb := "created"
	if result.Updated {
		verb = "updated"
	}
	provider := result.Provider
	if result.Mock {
		provider += " (mock)"
	}
	lines = append(lines,
		fmt.Sprintf("[s46] Share URL: %s", result.ViewerURL),
		fmt.Sprintf("[s46] Blob:      %s", result.BlobURL),
		fmt.Sprintf("[s46] %s encrypted share · TTL: %s · Provider: %s", verb, result.TTL, provider),
	)
	return app.renderer.Lines(lines...)
}

func inferredShareLines(session *sessioncmd.ListedSession) []string {
	if session == nil {
		return nil
	}
	label := "latest session"
	if session.Source == "local" {
		label = "latest local session"
	}
	parts := []string{displaySessionID(session.ID)}
	if session.Harness != "" {
		parts = append(parts, session.Harness)
	}
	if session.Model != "" {
		parts = append(parts, session.Model)
	}
	if task := compactSessionTask(session.Task, 48); task != "" {
		parts = append(parts, task)
	} else if session.Source != "local" && session.Location != "" {
		parts = append(parts, session.Location)
	}
	return []string{fmt.Sprintf("[s46] sharing %s: %s", label, strings.Join(parts, " · "))}
}

func runShareRevoke(ctx context.Context, app *app, target string) error {
	if err := app.requireCloudFeature("share revoke"); err != nil {
		return err
	}
	service := app.sessionService()
	var result sessioncmd.RevokeResult
	if err := app.withLock(ctx, func() error {
		var err error
		result, err = service.RevokeShare(ctx, target)
		return err
	}); err != nil {
		return err
	}
	if ok, err := app.writeStructured(result); ok {
		return err
	}
	return app.renderer.Lines(fmt.Sprintf("[s46] revoked share %s for %s", result.ID, result.SessionID))
}
