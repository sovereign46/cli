package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type MockFixtures struct {
	Account         string
	Team            string
	Lane            string
	Mode            string
	Endpoint        string
	Boxes           []string
	DefaultBox      string
	DefaultSession  string
	DefaultTask     string
	DefaultSpend    string
	DeviceCode      string
	UserCode        string
	VerificationURI string
}

var DefaultMockFixtures = MockFixtures{
	Account:         "dscape@acme.s46.dev",
	Team:            "acme",
	Lane:            "EU-OPO",
	Mode:            "cloud",
	Boxes:           []string{"box-01", "box-02"},
	DefaultBox:      "box-04.acme.s46.dev",
	DefaultSession:  "@dscape/auth-redirect-fix",
	DefaultTask:     "Fix auth redirect handling",
	DefaultSpend:    "€4.20",
	DeviceCode:      "mock-device-code",
	UserCode:        "WXYZ-1234",
	VerificationURI: "https://s46.dev/device",
}

type MockClient struct {
	Fixtures MockFixtures
}

func NewMockClient() *MockClient {
	return &MockClient{Fixtures: DefaultMockFixtures}
}

func NewLocalMockClient(baseURL string) *MockClient {
	origin, host := localMockOrigin(baseURL)
	fixtures := DefaultMockFixtures
	fixtures.Endpoint = origin
	fixtures.VerificationURI = origin + "/device"
	fixtures.DefaultBox = host
	return &MockClient{Fixtures: fixtures}
}

func localMockOrigin(baseURL string) (string, string) {
	if baseURL == "" {
		baseURL = "http://127.0.0.1:8080"
	}
	parsed, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "http://127.0.0.1:8080", "127.0.0.1:8080"
	}
	return parsed.Scheme + "://" + parsed.Host, parsed.Host
}

func (c *MockClient) StartDeviceLogin(ctx context.Context) (DeviceLogin, error) {
	fixtures := c.fixtures()
	return DeviceLogin{
		DeviceCode:      fixtures.DeviceCode,
		UserCode:        fixtures.UserCode,
		VerificationURI: fixtures.VerificationURI,
		Interval:        2 * time.Second,
		ExpiresAt:       time.Now().Add(10 * time.Minute).UTC(),
	}, nil
}

func (c *MockClient) PollDeviceLogin(ctx context.Context, deviceCode string, userHint string) (TokenSet, error) {
	account := userHint
	if account == "" {
		account = c.fixtures().Account
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
	fixtures := c.fixtures()
	return User{Email: fixtures.Account, Team: fixtures.Team}, nil
}

func (c *MockClient) Team(ctx context.Context, name string, opts TeamOptions) (Team, error) {
	fixtures := c.fixtures()
	team := sanitizeTeam(name)
	if team == "" {
		return Team{}, fmt.Errorf("team is required")
	}
	endpoint := opts.Endpoint
	if endpoint == "" {
		endpoint = fixtures.Endpoint
	}
	if endpoint == "" {
		endpoint = fmt.Sprintf("https://%s.s46.dev", team)
	}
	lane := opts.Lane
	if lane == "" {
		lane = fixtures.Lane
	}
	mode := opts.Mode
	if mode == "" {
		mode = fixtures.Mode
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
		Boxes:        append([]string(nil), fixtures.Boxes...),
		DefaultModel: model,
		Models:       append([]string(nil), DefaultModels...),
	}, nil
}

func (c *MockClient) Sessions(ctx context.Context, team Team) ([]Session, error) {
	session := DefaultSession(team)
	fixtures := c.fixtures()
	if fixtures.Endpoint != "" {
		session.Location = fixtures.DefaultBox
	}
	return []Session{session}, nil
}

func (c *MockClient) Detach(ctx context.Context, req DetachRequest) (Session, error) {
	harness := req.Harness
	if harness == "" {
		harness = "claude-code"
	}
	box := req.Box
	if box == "" {
		box = c.fixtures().DefaultBox
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

func (c *MockClient) Attach(ctx context.Context, req AttachRequest) (AttachResult, error) {
	fixtures := c.fixtures()
	scheme := "wss"
	host := fixtures.DefaultBox
	if fixtures.Endpoint != "" {
		parsed, err := url.Parse(fixtures.Endpoint)
		if err == nil && parsed.Host != "" {
			host = parsed.Host
			if parsed.Scheme == "http" {
				scheme = "ws"
			}
		}
	}
	return AttachResult{SessionID: req.SessionID, URL: fmt.Sprintf("%s://%s/session/%s", scheme, host, strings.TrimPrefix(req.SessionID, "@")), Protocol: "websocket"}, nil
}

func (c *MockClient) Land(ctx context.Context, req LandRequest) (LandResult, error) {
	title := req.Title
	fixtures := c.fixtures()
	if title == "" {
		title = fixtures.DefaultTask
	}
	session := req.Session
	if session.Harness == "" {
		session.Harness = req.Team.DefaultHarness()
	}
	if session.Model == "" {
		session.Model = req.Team.DefaultModel
	}
	if session.Spent == "" {
		session.Spent = fixtures.DefaultSpend
	}
	branchSlug := strings.TrimPrefix(req.SessionID, "@")
	branchSlug = strings.ReplaceAll(branchSlug, "/", "-")
	return LandResult{
		ID:      req.SessionID,
		Title:   title,
		Branch:  "s46/" + branchSlug,
		RanOn:   []string{"localhost", nonEmpty(session.Location, fixtures.DefaultBox), "localhost"},
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
		team.Name = DefaultMockFixtures.Team
	}
	if team.Lane == "" {
		team.Lane = DefaultMockFixtures.Lane
	}
	if team.DefaultModel == "" {
		team.DefaultModel = DefaultModel
	}
	return Session{
		ID:       DefaultMockFixtures.DefaultSession,
		State:    "running",
		Harness:  team.DefaultHarness(),
		Location: DefaultMockFixtures.DefaultBox,
		Lane:     team.Lane,
		Model:    team.DefaultModel,
		Age:      "14h",
		Spent:    DefaultMockFixtures.DefaultSpend,
	}
}

func (t Team) DefaultHarness() string {
	return "claude-code"
}

func (c *MockClient) fixtures() MockFixtures {
	if c == nil || c.Fixtures.Account == "" {
		return DefaultMockFixtures
	}
	fixtures := c.Fixtures
	if len(fixtures.Boxes) == 0 {
		fixtures.Boxes = DefaultMockFixtures.Boxes
	}
	if fixtures.DefaultBox == "" {
		fixtures.DefaultBox = DefaultMockFixtures.DefaultBox
	}
	if fixtures.DefaultSpend == "" {
		fixtures.DefaultSpend = DefaultMockFixtures.DefaultSpend
	}
	return fixtures
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
