package api

import (
	"context"
	"time"
)

const (
	DefaultModel        = "s46/deepseek-v4-flash-0731"
	DefaultTeam         = "@s46/engineering"
	DefaultAccount      = "dscape@s46.dev"
	DefaultGatewayURL   = "https://gateway.s46.dev"
	SessionSourceLocal  = "local"
	SessionSourceRemote = "remote"
)

func DefaultModelList() []string {
	return []string{DefaultModel}
}

// DeviceAuthAPI covers magic-link / device-auth flows.
type DeviceAuthAPI interface {
	StartDeviceLogin(ctx context.Context, req DeviceLoginRequest) (DeviceLogin, error)
	PollDeviceLogin(ctx context.Context, deviceCode string) (TokenSet, error)
	RefreshToken(ctx context.Context, refreshToken string, account string) (TokenSet, error)
}

// AccountAPI covers per-user account/device lookups.
type AccountAPI interface {
	Me(ctx context.Context, accessToken string) (User, error)
	Devices(ctx context.Context, accessToken string) ([]Device, error)
	DeleteDevice(ctx context.Context, deviceID string, accessToken string) error
}

// TeamAPI returns workspace/team metadata.
type TeamAPI interface {
	Team(ctx context.Context, name string, opts TeamOptions) (Team, error)
}

// SessionAPI covers listing coding-agent sessions.
type SessionAPI interface {
	Sessions(ctx context.Context, team Team, accessToken string) ([]Session, error)
}

// Client is the umbrella API used by application services that span
// multiple subdomains. New code should prefer the narrowest interface
// it actually needs (DeviceAuthAPI, AccountAPI, TeamAPI, SessionAPI)
// so tests can pass tighter fakes.
type Client interface {
	DeviceAuthAPI
	AccountAPI
	TeamAPI
	SessionAPI
}

type DeviceLoginRequest struct {
	Email      string `json:"email"`
	DeviceID   string `json:"deviceId"`
	DeviceName string `json:"deviceName"`
}

type DeviceLogin struct {
	DeviceCode      string        `json:"deviceCode"`
	UserCode        string        `json:"userCode"`
	VerificationURI string        `json:"verificationUri"`
	Interval        time.Duration `json:"interval"`
	ExpiresAt       time.Time     `json:"expiresAt"`
}

type TokenSet struct {
	Account      string    `json:"account"`
	Organization string    `json:"organization,omitempty"`
	DeviceID     string    `json:"deviceId"`
	AccessToken  string    `json:"accessToken"`
	RefreshToken string    `json:"refreshToken"`
	ExpiresAt    time.Time `json:"expiresAt"`
}

type Device struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	CreatedAt  time.Time `json:"createdAt"`
	LastSeenAt time.Time `json:"lastSeenAt"`
	LastSeenIP string    `json:"lastSeenIp,omitempty"`
}

type User struct {
	Email        string `json:"email"`
	Organization string `json:"organization,omitempty"`
	Team         string `json:"team"`
}

type TeamOptions struct {
	Endpoint     string
	Region       string
	DefaultModel string
	AccessToken  string
}

type ModelRuntime struct {
	Engine                 string `json:"engine,omitempty"`
	Version                string `json:"version,omitempty"`
	ProfileSHA256          string `json:"profileSha256,omitempty"`
	ContextWindow          int    `json:"contextWindow,omitempty"`
	MaxTokens              int    `json:"maxTokens,omitempty"`
	Reasoning              bool   `json:"reasoning"`
	MinMemoryBytes         int64  `json:"minMemoryBytes,omitempty"`
	RecommendedMemoryBytes int64  `json:"recommendedMemoryBytes,omitempty"`
}

type Team struct {
	Name          string                  `json:"name"`
	Endpoint      string                  `json:"endpoint"`
	Region        string                  `json:"region"`
	WorkerHosts   []string                `json:"workerHosts"`
	DefaultModel  string                  `json:"defaultModel"`
	Models        []string                `json:"models"`
	ModelMetadata map[string]ModelRuntime `json:"modelMetadata"`
}

type Session struct {
	ID       string `json:"id"`
	State    string `json:"state"`
	Harness  string `json:"harness"`
	Location string `json:"location"`
	Region   string `json:"region"`
	Model    string `json:"model"`
	Age      string `json:"age"`
	Spent    string `json:"spent"`
	Task     string `json:"task,omitempty"`
}
