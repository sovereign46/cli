//go:build !release

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

type MockFixtures struct {
	Account         string
	Team            string
	Region          string
	Mode            string
	Endpoint        string
	WorkerHosts     []string
	DefaultBox      string
	DefaultSession  string
	DefaultTask     string
	DefaultSpend    string
	DeviceCode      string
	UserCode        string
	VerificationURI string
}

func defaultMockFixtures() MockFixtures {
	return MockFixtures{
		Account:         DefaultAccount,
		Team:            DefaultTeam,
		Region:          "EU-OPO",
		Mode:            "cloud",
		WorkerHosts:     []string{"worker-01", "worker-02"},
		DefaultBox:      "worker-04.eu-opo.s46.dev",
		DefaultSession:  "@dscape/auth-redirect-fix",
		DefaultTask:     "Fix auth redirect handling",
		DefaultSpend:    "€4.20",
		DeviceCode:      "mock-device-code",
		UserCode:        "WXYZ-1234",
		VerificationURI: "https://s46.dev/v1/auth/magic/consume",
	}
}

type MockClient struct {
	Fixtures     MockFixtures
	Models       []string
	LastLogin    DeviceLoginRequest
	RevokedIDs   map[string]bool
	LastDeviceID string
}

func NewMockClient() *MockClient {
	return &MockClient{Fixtures: defaultMockFixtures(), Models: DefaultModelList()}
}

func newMockClientFromEnv(map[string]string) (Client, error) {
	return NewMockClient(), nil
}

func (c *MockClient) StartDeviceLogin(ctx context.Context, req DeviceLoginRequest) (DeviceLogin, error) {
	fixtures := c.fixtures()
	if req.Email == "" {
		req.Email = fixtures.Account
	}
	if req.DeviceID == "" {
		req.DeviceID = "mock-device"
	}
	if req.DeviceName == "" {
		req.DeviceName = req.DeviceID
	}
	c.LastLogin = req
	c.LastDeviceID = req.DeviceID
	return DeviceLogin{
		DeviceCode:      fixtures.DeviceCode,
		UserCode:        fixtures.UserCode,
		VerificationURI: fixtures.VerificationURI,
		Interval:        2 * time.Second,
		ExpiresAt:       time.Now().Add(10 * time.Minute).UTC(),
	}, nil
}

func (c *MockClient) PollDeviceLogin(ctx context.Context, deviceCode string) (TokenSet, error) {
	account := c.LastLogin.Email
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

func (c *MockClient) Devices(ctx context.Context, accessToken string) ([]Device, error) {
	deviceID := c.LastDeviceID
	if deviceID == "" {
		deviceID = "mock-device"
	}
	if c.RevokedIDs != nil && c.RevokedIDs[deviceID] {
		return []Device{}, nil
	}
	name := c.LastLogin.DeviceName
	if name == "" {
		name = "Mock device"
	}
	now := time.Now().UTC()
	return []Device{{ID: deviceID, Name: name, CreatedAt: now, LastSeenAt: now, LastSeenIP: "127.0.0.1"}}, nil
}

func (c *MockClient) DeleteDevice(ctx context.Context, deviceID string, accessToken string) error {
	if c.RevokedIDs == nil {
		c.RevokedIDs = map[string]bool{}
	}
	c.RevokedIDs[deviceID] = true
	return nil
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
		endpoint = DefaultGatewayURL
	}
	region := opts.Region
	if region == "" {
		region = fixtures.Region
	}
	model := opts.DefaultModel
	if model == "" {
		model = DefaultModel
	}
	return Team{
		Name:         team,
		Endpoint:     endpoint,
		Region:       region,
		WorkerHosts:  append([]string(nil), fixtures.WorkerHosts...),
		DefaultModel: model,
		Models:       c.modelList(),
	}, nil
}

func (c *MockClient) modelList() []string {
	if c != nil && len(c.Models) > 0 {
		return c.Models
	}
	return DefaultModelList()
}

func (c *MockClient) Sessions(ctx context.Context, team Team, accessToken string) ([]Session, error) {
	session := defaultMockSession(team)
	fixtures := c.fixtures()
	if fixtures.Endpoint != "" {
		session.Location = fixtures.DefaultBox
	}
	return []Session{session}, nil
}

func defaultMockSession(team Team) Session {
	defaults := defaultMockFixtures()
	if team.Name == "" {
		team.Name = defaults.Team
	}
	if team.Region == "" {
		team.Region = defaults.Region
	}
	if team.DefaultModel == "" {
		team.DefaultModel = DefaultModel
	}
	return Session{
		ID:       defaults.DefaultSession,
		State:    "running",
		Harness:  team.DefaultHarness(),
		Location: defaults.DefaultBox,
		Region:   team.Region,
		Model:    team.DefaultModel,
		Age:      "14h",
		Spent:    defaults.DefaultSpend,
		Task:     defaults.DefaultTask,
	}
}

func (c *MockClient) fixtures() MockFixtures {
	defaults := defaultMockFixtures()
	if c == nil || c.Fixtures.Account == "" {
		return defaults
	}
	fixtures := c.Fixtures
	if len(fixtures.WorkerHosts) == 0 {
		fixtures.WorkerHosts = defaults.WorkerHosts
	}
	if fixtures.DefaultBox == "" {
		fixtures.DefaultBox = defaults.DefaultBox
	}
	if fixtures.DefaultSpend == "" {
		fixtures.DefaultSpend = defaults.DefaultSpend
	}
	return fixtures
}

func (c *MockClient) tokenSet(account string) TokenSet {
	deviceID := c.LastDeviceID
	if deviceID == "" {
		deviceID = "mock-device"
	}
	return TokenSet{
		Account:      account,
		DeviceID:     deviceID,
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
	if value == "" {
		return ""
	}
	if !strings.HasPrefix(value, "@") || strings.Count(value, "/") != 1 {
		return ""
	}
	parts := strings.Split(strings.TrimPrefix(value, "@"), "/")
	org := regexp.MustCompile(`[^a-z0-9-]+`).ReplaceAllString(parts[0], "")
	team := regexp.MustCompile(`[^a-z0-9-]+`).ReplaceAllString(parts[1], "")
	if org == "" || team == "" {
		return ""
	}
	return "@" + org + "/" + team
}
