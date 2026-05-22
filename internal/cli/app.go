package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"

	"github.com/sovereign46/cli/internal/auth"
	"github.com/sovereign46/cli/internal/config"
	sessioncmd "github.com/sovereign46/cli/internal/session"
)

type inputReader struct {
	*bufio.Reader
	source io.Reader
}

func authStatus(state config.State) string {
	if state.Authenticated && state.CurrentUser != "" {
		return state.CurrentUser
	}
	return "not authenticated"
}

func (a *app) requireAccessToken(ctx context.Context) (string, error) {
	state, err := a.config.LoadState()
	if err != nil {
		return "", err
	}
	if !state.Authenticated || state.CurrentUser == "" {
		return "", fmt.Errorf("not authenticated; run `s46 login` before connecting a cloud team")
	}
	token, err := a.authService().AccessToken(ctx)
	if err != nil {
		return "", fmt.Errorf("could not obtain s46 access token: %w; run `s46 login` if your session expired", err)
	}
	if token == "" {
		return "", fmt.Errorf("not authenticated; run `s46 login` before connecting a cloud team")
	}
	return token, nil
}

// authService wires the standard {API, Config, Keyring} triple into
// auth.Service. It is intentionally a method, not cached on app, so each
// caller gets a fresh value (auth.Service is a small struct).
func (a *app) authService() auth.Service {
	return auth.Service{API: a.api, Config: a.config, Keyring: a.keyring}
}

// sessionService wires session.Service with the same triple plus an
// auth-token provider so session can fetch bearers without reaching
// into the keyring directly.
func (a *app) sessionService() sessioncmd.Service {
	return sessioncmd.Service{API: a.api, Auth: a.authService(), Config: a.config, Keyring: a.keyring, Harness: a.harness}
}

func (a *app) writeStructured(value any) (bool, error) {
	if a.options.json {
		return true, a.renderer.WriteJSON(value)
	}
	if a.options.jsonl {
		return true, a.renderer.WriteJSONL(value)
	}
	return false, nil
}

func (a *app) requireCloudFeature(feature string) error {
	cfg, err := a.config.LoadConfig()
	if err != nil {
		return err
	}
	if cfg.ActiveMode() != config.ModeAirplane {
		return nil
	}
	return fmt.Errorf("%s requires cloud connectivity; go online and switch to cloud mode to use it. Airplane mode supports local coding only", feature)
}

func (a *app) stdinReader() *inputReader {
	if a.promptReader == nil {
		a.promptReader = &inputReader{Reader: bufio.NewReader(a.runtime.Stdin), source: a.runtime.Stdin}
	}
	return a.promptReader
}

func (a *app) withLock(ctx context.Context, fn func() error) error {
	lock, err := a.config.Lock(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Unlock() }()
	return fn()
}

func (a *app) debug(format string, args ...any) {
	if a != nil && a.options != nil && a.options.verbose && !a.options.machineReadable() {
		fmt.Fprintf(a.runtime.Stderr, "[s46:debug] "+format+"\n", args...)
	}
}
