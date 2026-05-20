package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/sovereign46/s46-cli/internal/api"
	"github.com/sovereign46/s46-cli/internal/config"
	"github.com/sovereign46/s46-cli/internal/keyring"
	"github.com/sovereign46/s46-cli/internal/strs"
)

const (
	// TokenService is the keyring service name under which user bearer
	// tokens are stored. Exported so test helpers and any future consumers
	// can write or read tokens with a stable key.
	TokenService = "s46.tokens"

	defaultAirplaneToken = "s46_airplane_local"
)

type Service struct {
	API     api.Client
	Config  *config.Store
	Keyring keyring.Store
}

type LoginResult struct {
	Authenticated   bool      `json:"authenticated"`
	User            string    `json:"user"`
	Team            string    `json:"team"`
	DeviceID        string    `json:"deviceId"`
	DeviceName      string    `json:"deviceName"`
	VerificationURI string    `json:"verificationUri"`
	UserCode        string    `json:"userCode"`
	ExpiresAt       time.Time `json:"expiresAt"`
}

type LoginRequest struct {
	Email      string
	Team       string
	DeviceID   string
	DeviceName string
}

type DeviceCallback func(api.DeviceLogin) error

func (s Service) Login(ctx context.Context, userHint string, teamHint string) (LoginResult, error) {
	return s.LoginWithDeviceCallback(ctx, LoginRequest{Email: userHint, Team: teamHint}, nil)
}

func (s Service) LoginWithDeviceCallback(ctx context.Context, req LoginRequest, onDevice DeviceCallback) (LoginResult, error) {
	if req.Email == "" && req.Team == "" && req.DeviceID == "" && req.DeviceName == "" {
		if result, ok := s.currentLogin(ctx); ok {
			return result, nil
		}
	}

	loginReq, err := s.resolveLoginRequest(req)
	if err != nil {
		return LoginResult{}, err
	}
	device, err := s.API.StartDeviceLogin(ctx, loginReq)
	if err != nil {
		if errors.Is(err, api.ErrNotInvited) {
			return LoginResult{}, fmt.Errorf("%s is not invited to Sovereign46; ask an admin for an invitation", loginReq.Email)
		}
		return LoginResult{}, err
	}
	if onDevice != nil {
		if err := onDevice(device); err != nil {
			return LoginResult{}, err
		}
	}
	tokens, err := s.pollDeviceLogin(ctx, device)
	if err != nil {
		return LoginResult{}, err
	}

	user, err := s.API.Me(ctx, tokens.AccessToken)
	if err != nil {
		return LoginResult{}, fmt.Errorf("login failed after approval: could not load account details from /v1/me: %w", err)
	}
	account := strs.FirstNonEmpty(user.Email, tokens.Account)
	tokens.Account = account
	if err := s.storeTokens(ctx, tokens); err != nil {
		return LoginResult{}, err
	}
	teamName := user.Team
	if teamName == "" {
		return LoginResult{}, fmt.Errorf("login failed after approval: API did not return a team for %s", account)
	}
	if req.Team != "" && req.Team != teamName {
		return LoginResult{}, fmt.Errorf("login failed: %s belongs to team %s, not requested team %s", account, teamName, req.Team)
	}
	team, err := s.API.Team(ctx, teamName, api.TeamOptions{AccessToken: tokens.AccessToken})
	if err != nil {
		if errors.Is(err, api.ErrForbidden) {
			return LoginResult{}, fmt.Errorf("login failed: authenticated as %s for team %s, but the API denied team lookup; ask an admin to check your team membership", account, teamName)
		}
		return LoginResult{}, fmt.Errorf("login failed: could not load team %s: %w", teamName, err)
	}

	cfg, err := s.Config.LoadConfig()
	if err != nil {
		return LoginResult{}, err
	}
	if _, ok := cfg.Teams[team.Name]; !ok {
		cfg.Teams[team.Name] = config.TeamConfigFromAPI(team, "standard", team.DefaultModel, config.ModeCloud)
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
	state.CurrentUser = account
	state.CurrentDeviceID = strs.FirstNonEmpty(tokens.DeviceID, loginReq.DeviceID)
	state.CurrentDeviceName = loginReq.DeviceName
	state.LastLoginAt = time.Now().UTC().Format(time.RFC3339)
	if err := s.Config.SaveState(state); err != nil {
		return LoginResult{}, err
	}

	return LoginResult{
		Authenticated:   true,
		User:            account,
		Team:            team.Name,
		DeviceID:        state.CurrentDeviceID,
		DeviceName:      state.CurrentDeviceName,
		VerificationURI: device.VerificationURI,
		UserCode:        device.UserCode,
		ExpiresAt:       tokens.ExpiresAt,
	}, nil
}

func (s Service) resolveLoginRequest(req LoginRequest) (api.DeviceLoginRequest, error) {
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if req.Email == "" {
		return api.DeviceLoginRequest{}, fmt.Errorf("email is required; pass --user <email>")
	}
	state, err := s.Config.LoadState()
	if err != nil {
		return api.DeviceLoginRequest{}, err
	}
	deviceID := strs.FirstNonEmpty(req.DeviceID, strs.EnvValue(s.Config.Env, "S46_DEVICE_ID"), state.CurrentDeviceID, defaultDeviceID(s.Config.Env))
	deviceID = sanitizeDeviceID(deviceID)
	if deviceID == "" {
		return api.DeviceLoginRequest{}, fmt.Errorf("device id is required; pass --device-id <id>")
	}
	deviceName := strs.FirstNonEmpty(req.DeviceName, strs.EnvValue(s.Config.Env, "S46_DEVICE_NAME"), state.CurrentDeviceName, defaultDeviceName(deviceID))
	return api.DeviceLoginRequest{Email: req.Email, DeviceID: deviceID, DeviceName: deviceName}, nil
}

func (s Service) CurrentLogin(ctx context.Context) (LoginResult, bool) {
	return s.currentLogin(ctx)
}

func (s Service) currentLogin(ctx context.Context) (LoginResult, bool) {
	if s.Keyring == nil {
		return LoginResult{}, false
	}
	state, err := s.Config.LoadState()
	if err != nil || !state.Authenticated || state.CurrentUser == "" {
		return LoginResult{}, false
	}
	tokens, err := s.loadTokens(ctx, state.CurrentUser)
	if err != nil || tokens.AccessToken == "" {
		return LoginResult{}, false
	}
	if !tokens.ExpiresAt.IsZero() && time.Until(tokens.ExpiresAt) < 30*time.Second {
		if tokens.RefreshToken == "" {
			return LoginResult{}, false
		}
		tokens, err = s.API.RefreshToken(ctx, tokens.RefreshToken, state.CurrentUser)
		if err != nil || tokens.AccessToken == "" {
			return LoginResult{}, false
		}
		if err := s.storeTokens(ctx, tokens); err != nil {
			return LoginResult{}, false
		}
	}
	cfg, err := s.Config.LoadConfig()
	if err != nil {
		return LoginResult{}, false
	}
	return LoginResult{
		Authenticated: true,
		User:          state.CurrentUser,
		Team:          cfg.ActiveTeam,
		DeviceID:      strs.FirstNonEmpty(tokens.DeviceID, state.CurrentDeviceID),
		DeviceName:    state.CurrentDeviceName,
		ExpiresAt:     tokens.ExpiresAt,
	}, true
}

func (s Service) pollDeviceLogin(ctx context.Context, device api.DeviceLogin) (api.TokenSet, error) {
	interval := device.Interval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	for {
		if !device.ExpiresAt.IsZero() && time.Now().After(device.ExpiresAt) {
			return api.TokenSet{}, fmt.Errorf("device login expired; run `s46 login` again")
		}
		tokens, err := s.API.PollDeviceLogin(ctx, device.DeviceCode)
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
	return user, s.clearLocalCredentials(ctx, state)
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
	if s.airplaneMode() {
		return s.airplaneToken(), nil
	}
	_, tokens, err := s.currentTokenSet(ctx, refresh)
	if err != nil {
		return "", err
	}
	return tokens.AccessToken, nil
}

// AccessToken returns a valid bearer access token for the current user,
// refreshing it transparently if it is within 30 seconds of expiry. In
// airplane mode it returns an empty string with no error so that callers
// fall through to local-gateway calls without a bearer. When no keyring
// is configured (typical in lightweight tests) it returns an empty token
// rather than panicking, mirroring "no credentials available" semantics.
func (s Service) AccessToken(ctx context.Context) (string, error) {
	if s.airplaneMode() {
		return "", nil
	}
	if s.Keyring == nil {
		return "", nil
	}
	_, tokens, err := s.currentTokenSet(ctx, false)
	if err != nil {
		return "", err
	}
	return tokens.AccessToken, nil
}

func (s Service) Devices(ctx context.Context) ([]api.Device, error) {
	_, tokens, err := s.currentTokenSet(ctx, false)
	if err != nil {
		return nil, err
	}
	return s.API.Devices(ctx, tokens.AccessToken)
}

func (s Service) DeleteDevice(ctx context.Context, deviceID string) (bool, error) {
	state, tokens, err := s.currentTokenSet(ctx, false)
	if err != nil {
		return false, err
	}
	requestedID := sanitizeDeviceID(deviceID)
	if requestedID == "" {
		return false, fmt.Errorf("device id is required")
	}
	if err := s.API.DeleteDevice(ctx, requestedID, tokens.AccessToken); err != nil {
		return false, err
	}
	currentID := sanitizeDeviceID(strs.FirstNonEmpty(state.CurrentDeviceID, tokens.DeviceID))
	if requestedID == currentID {
		return true, s.clearLocalCredentials(ctx, state)
	}
	return false, nil
}

func (s Service) currentTokenSet(ctx context.Context, refresh bool) (config.State, api.TokenSet, error) {
	state, err := s.Config.LoadState()
	if err != nil {
		return config.State{}, api.TokenSet{}, err
	}
	if state.CurrentUser == "" {
		return config.State{}, api.TokenSet{}, fmt.Errorf("no refresh token available; run `s46 login` first")
	}
	tokens, err := s.loadTokens(ctx, state.CurrentUser)
	if err != nil {
		return config.State{}, api.TokenSet{}, fmt.Errorf("no refresh token available; run `s46 login` first")
	}
	if refresh || time.Until(tokens.ExpiresAt) < 30*time.Second {
		tokens, err = s.API.RefreshToken(ctx, tokens.RefreshToken, state.CurrentUser)
		if err != nil {
			return config.State{}, api.TokenSet{}, err
		}
		if err := s.storeTokens(ctx, tokens); err != nil {
			return config.State{}, api.TokenSet{}, err
		}
	}
	return state, tokens, nil
}

func (s Service) clearLocalCredentials(ctx context.Context, state config.State) error {
	if state.CurrentUser != "" {
		if err := s.Keyring.Delete(ctx, TokenService, state.CurrentUser); err != nil {
			return err
		}
	}
	state.Authenticated = false
	state.CurrentUser = ""
	state.CurrentDeviceID = ""
	state.CurrentDeviceName = ""
	state.LastLoginAt = ""
	return s.Config.SaveState(state)
}

func (s Service) storeTokens(ctx context.Context, tokens api.TokenSet) error {
	raw, err := json.Marshal(tokens)
	if err != nil {
		return err
	}
	return s.Keyring.Set(ctx, TokenService, tokens.Account, string(raw))
}

func (s Service) loadTokens(ctx context.Context, account string) (api.TokenSet, error) {
	raw, err := s.Keyring.Get(ctx, TokenService, account)
	if err != nil {
		return api.TokenSet{}, err
	}
	var tokens api.TokenSet
	if err := json.Unmarshal([]byte(raw), &tokens); err != nil {
		return api.TokenSet{}, err
	}
	return tokens, nil
}

func (s Service) airplaneMode() bool {
	if s.Config == nil {
		return false
	}
	cfg, err := s.Config.LoadConfig()
	if err != nil {
		return false
	}
	return cfg.ActiveMode() == config.ModeAirplane
}

func (s Service) airplaneToken() string {
	if s.Config != nil {
		return strs.FirstNonEmpty(strs.EnvValue(s.Config.Env, "S46_AIRPLANE_TOKEN"), defaultAirplaneToken)
	}
	return defaultAirplaneToken
}

func sanitizeDeviceID(value string) string {
	value = strings.TrimSpace(value)
	value = regexp.MustCompile(`[^a-zA-Z0-9._:-]+`).ReplaceAllString(value, "-")
	value = strings.Trim(value, "-._:")
	if len(value) > 128 {
		value = value[:128]
	}
	return value
}

func defaultDeviceID(env map[string]string) string {
	if value := strs.EnvValue(env, "HOSTNAME"); value != "" {
		return value
	}
	if host, err := os.Hostname(); err == nil && host != "" {
		return host
	}
	return "default-device"
}

func defaultDeviceName(deviceID string) string {
	if host, err := os.Hostname(); err == nil && host != "" {
		return host
	}
	return deviceID
}
