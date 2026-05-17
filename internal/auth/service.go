package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sovereign46/s46-cli/internal/api"
	"github.com/sovereign46/s46-cli/internal/config"
	"github.com/sovereign46/s46-cli/internal/keyring"
)

const tokenService = "s46.tokens"

type Service struct {
	API     api.Client
	Config  *config.Store
	Keyring keyring.Store
}

type LoginResult struct {
	Authenticated   bool      `json:"authenticated"`
	User            string    `json:"user"`
	Team            string    `json:"team"`
	VerificationURI string    `json:"verificationUri"`
	UserCode        string    `json:"userCode"`
	ExpiresAt       time.Time `json:"expiresAt"`
	Mock            bool      `json:"mock"`
}

type DeviceCallback func(api.DeviceLogin) error

func (s Service) Login(ctx context.Context, userHint string, teamHint string) (LoginResult, error) {
	return s.LoginWithDeviceCallback(ctx, userHint, teamHint, nil)
}

func (s Service) LoginWithDeviceCallback(ctx context.Context, userHint string, teamHint string, onDevice DeviceCallback) (LoginResult, error) {
	device, err := s.API.StartDeviceLogin(ctx)
	if err != nil {
		return LoginResult{}, err
	}
	if onDevice != nil {
		if err := onDevice(device); err != nil {
			return LoginResult{}, err
		}
	}
	tokens, err := s.pollDeviceLogin(ctx, device, userHint)
	if err != nil {
		return LoginResult{}, err
	}
	if err := s.storeTokens(ctx, tokens); err != nil {
		return LoginResult{}, err
	}

	teamName := teamHint
	if teamName == "" {
		teamName = TeamFromEmail(tokens.Account)
	}
	team, err := s.API.Team(ctx, teamName, api.TeamOptions{AccessToken: tokens.AccessToken})
	if err != nil {
		return LoginResult{}, err
	}

	cfg, err := s.Config.LoadConfig()
	if err != nil {
		return LoginResult{}, err
	}
	if _, ok := cfg.Teams[team.Name]; !ok {
		cfg.Teams[team.Name] = config.TeamConfigFromAPI(team, "claude-code", team.DefaultModel)
	}
	cfg.ActiveTeam = team.Name
	if err := s.Config.SaveConfig(cfg); err != nil {
		return LoginResult{}, err
	}

	state, err := s.Config.LoadState()
	if err != nil {
		return LoginResult{}, err
	}
	state.Authenticated = true
	state.CurrentUser = tokens.Account
	state.LastLoginAt = time.Now().UTC().Format(time.RFC3339)
	if err := s.Config.SaveState(state); err != nil {
		return LoginResult{}, err
	}

	return LoginResult{
		Authenticated:   true,
		User:            tokens.Account,
		Team:            team.Name,
		VerificationURI: device.VerificationURI,
		UserCode:        device.UserCode,
		ExpiresAt:       tokens.ExpiresAt,
		Mock:            true,
	}, nil
}

func (s Service) pollDeviceLogin(ctx context.Context, device api.DeviceLogin, userHint string) (api.TokenSet, error) {
	interval := device.Interval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	for {
		if !device.ExpiresAt.IsZero() && time.Now().After(device.ExpiresAt) {
			return api.TokenSet{}, fmt.Errorf("device login expired; run `s46 login` again")
		}
		tokens, err := s.API.PollDeviceLogin(ctx, device.DeviceCode, userHint)
		if err == nil {
			return tokens, nil
		}
		if !errors.Is(err, api.ErrAuthorizationPending) {
			if errors.Is(err, api.ErrExpired) {
				return api.TokenSet{}, fmt.Errorf("device login expired; run `s46 login` again")
			}
			return api.TokenSet{}, err
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return api.TokenSet{}, ctx.Err()
		case <-timer.C:
		}
	}
}

func (s Service) Logout(ctx context.Context) (string, error) {
	state, err := s.Config.LoadState()
	if err != nil {
		return "", err
	}
	user := state.CurrentUser
	if user != "" {
		if err := s.Keyring.Delete(ctx, tokenService, user); err != nil {
			return "", err
		}
	}
	state.Authenticated = false
	state.CurrentUser = ""
	state.LastLoginAt = ""
	return user, s.Config.SaveState(state)
}

func (s Service) Whoami(ctx context.Context) (string, error) {
	state, err := s.Config.LoadState()
	if err != nil {
		return "", err
	}
	if !state.Authenticated || state.CurrentUser == "" {
		return "", fmt.Errorf("not authenticated; run `s46 login` first")
	}
	return state.CurrentUser, nil
}

func (s Service) Token(ctx context.Context, refresh bool) (string, error) {
	state, err := s.Config.LoadState()
	if err != nil {
		return "", err
	}
	if state.CurrentUser == "" {
		return "", fmt.Errorf("no refresh token available; run `s46 login` first")
	}
	tokens, err := s.loadTokens(ctx, state.CurrentUser)
	if err != nil {
		return "", fmt.Errorf("no refresh token available; run `s46 login` first")
	}
	if refresh || time.Until(tokens.ExpiresAt) < 30*time.Second {
		tokens, err = s.API.RefreshToken(ctx, tokens.RefreshToken, state.CurrentUser)
		if err != nil {
			return "", err
		}
		if err := s.storeTokens(ctx, tokens); err != nil {
			return "", err
		}
	}
	return tokens.AccessToken, nil
}

func (s Service) storeTokens(ctx context.Context, tokens api.TokenSet) error {
	raw, err := json.Marshal(tokens)
	if err != nil {
		return err
	}
	return s.Keyring.Set(ctx, tokenService, tokens.Account, string(raw))
}

func (s Service) loadTokens(ctx context.Context, account string) (api.TokenSet, error) {
	raw, err := s.Keyring.Get(ctx, tokenService, account)
	if err != nil {
		return api.TokenSet{}, err
	}
	var tokens api.TokenSet
	if err := json.Unmarshal([]byte(raw), &tokens); err != nil {
		return api.TokenSet{}, err
	}
	return tokens, nil
}

func TeamFromEmail(email string) string {
	parts := strings.Split(email, "@")
	if len(parts) != 2 {
		return "acme"
	}
	domain := parts[1]
	if strings.HasSuffix(domain, ".s46.dev") {
		return strings.TrimSuffix(domain, ".s46.dev")
	}
	team := strings.Split(domain, ".")[0]
	if team == "" || team == "s46" {
		return "acme"
	}
	return team
}
