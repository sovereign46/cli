package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"
)

type MockClient struct{}

func NewMockClient() *MockClient {
	return &MockClient{}
}

func (c *MockClient) StartDeviceLogin(ctx context.Context) (DeviceLogin, error) {
	return DeviceLogin{
		DeviceCode:      "mock-device-code",
		UserCode:        "WXYZ-1234",
		VerificationURI: "https://s46.dev/device",
		Interval:        2 * time.Second,
		ExpiresAt:       time.Now().Add(10 * time.Minute).UTC(),
	}, nil
}

func (c *MockClient) PollDeviceLogin(ctx context.Context, deviceCode string, userHint string) (TokenSet, error) {
	account := userHint
	if account == "" {
		account = "dscape@acme.s46.dev"
	}
	return c.tokenSet(account), nil
}

func (c *MockClient) RefreshToken(ctx context.Context, refreshToken string, account string) (TokenSet, error) {
	if account == "" {
		return TokenSet{}, fmt.Errorf("account is required to refresh token")
	}
	return c.tokenSet(account), nil
}

func (c *MockClient) Me(ctx context.Context, accessToken string) (User, error) {
	return User{Email: "dscape@acme.s46.dev", Team: "acme"}, nil
}

func (c *MockClient) Team(ctx context.Context, name string, opts TeamOptions) (Team, error) {
	team := sanitizeTeam(name)
	if team == "" {
		return Team{}, fmt.Errorf("team is required")
	}
	endpoint := opts.Endpoint
	if endpoint == "" {
		endpoint = fmt.Sprintf("https://%s.s46.dev", team)
	}
	lane := opts.Lane
	if lane == "" {
		lane = "EU-OPO"
	}
	mode := opts.Mode
	if mode == "" {
		mode = "cloud"
	}
	model := opts.DefaultModel
	if model == "" {
		model = DefaultModel
	}
	return Team{
		Name:         team,
		Endpoint:     endpoint,
		Lane:         lane,
		Mode:         mode,
		Boxes:        []string{"box-01", "box-02"},
		DefaultModel: model,
		Models:       append([]string(nil), DefaultModels...),
	}, nil
}

func (c *MockClient) Sessions(ctx context.Context, team Team) ([]Session, error) {
	return []Session{DefaultSession(team)}, nil
}

func (c *MockClient) Detach(ctx context.Context, req DetachRequest) (Session, error) {
	harness := req.Harness
	if harness == "" {
		harness = "claude-code"
	}
	box := req.Box
	if box == "" {
		box = "box-04.acme.s46.dev"
	}
	return Session{
		ID:       req.SessionID,
		State:    "running",
		Harness:  harness,
		Location: box,
		Lane:     req.Team.Lane,
		Model:    req.Team.DefaultModel,
		Age:      "0m",
		Spent:    "€0.00",
	}, nil
}

func (c *MockClient) Resume(ctx context.Context, req ResumeRequest) (Session, error) {
	session := req.Session
	session.ID = req.SessionID
	session.State = "resumed"
	session.Location = "localhost"
	session.Age = "0m"
	return session, nil
}

func (c *MockClient) Land(ctx context.Context, req LandRequest) (LandResult, error) {
	title := req.Title
	if title == "" {
		title = "Fix auth redirect handling"
	}
	session := req.Session
	if session.Harness == "" {
		session.Harness = req.Team.DefaultHarness()
	}
	if session.Model == "" {
		session.Model = req.Team.DefaultModel
	}
	if session.Spent == "" {
		session.Spent = "€4.20"
	}
	branchSlug := strings.TrimPrefix(req.SessionID, "@")
	branchSlug = strings.ReplaceAll(branchSlug, "/", "-")
	return LandResult{
		ID:      req.SessionID,
		Title:   title,
		Branch:  "s46/" + branchSlug,
		RanOn:   []string{"localhost", nonEmpty(session.Location, "box-04.acme.s46.dev"), "localhost"},
		Harness: session.Harness,
		Model:   session.Model,
		Cost:    session.Spent,
		Review: ReviewPacket{
			Summary:           fmt.Sprintf("%s. Prepared from %s for human review.", title, req.SessionID),
			Checklist:         []string{"inspect git diff", "run tests", "review generated summary", "open PR"},
			SuggestedCommands: []string{"git diff", "git status", "gh pr create --fill"},
		},
	}, nil
}

func DefaultSession(team Team) Session {
	if team.Name == "" {
		team.Name = "acme"
	}
	if team.Lane == "" {
		team.Lane = "EU-OPO"
	}
	if team.DefaultModel == "" {
		team.DefaultModel = DefaultModel
	}
	return Session{
		ID:       "@dscape/auth-redirect-fix",
		State:    "running",
		Harness:  team.DefaultHarness(),
		Location: "box-04.acme.s46.dev",
		Lane:     team.Lane,
		Model:    team.DefaultModel,
		Age:      "14h",
		Spent:    "€4.20",
	}
}

func (t Team) DefaultHarness() string {
	return "claude-code"
}

func (c *MockClient) tokenSet(account string) TokenSet {
	return TokenSet{
		Account:      account,
		AccessToken:  "s46_mock_access_" + safeTokenPart(account) + "_" + randomHex(8),
		RefreshToken: "s46_mock_refresh_" + safeTokenPart(account),
		ExpiresAt:    time.Now().Add(time.Hour).UTC(),
	}
}

func randomHex(bytes int) string {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func safeTokenPart(value string) string {
	return regexp.MustCompile(`[^a-zA-Z0-9]+`).ReplaceAllString(value, "_")
}

func sanitizeTeam(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	return regexp.MustCompile(`[^a-z0-9-]+`).ReplaceAllString(value, "")
}

func nonEmpty(value string, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
