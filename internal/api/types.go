package api

import (
	"context"
	"time"
)

const DefaultModel = "s46/kimi-k2.6"

var DefaultModels = []string{
	"s46/kimi-k2.6",
	"s46/gemma-3",
	"s46/deepseek-coder-v3",
	"s46/qwen3-coder",
	"s46/mistral-large",
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

// SessionAPI covers lifecycle operations on coding-agent sessions.
type SessionAPI interface {
	Sessions(ctx context.Context, team Team, accessToken string) ([]Session, error)
	Detach(ctx context.Context, req DetachRequest) (Session, error)
	Resume(ctx context.Context, req ResumeRequest) (Session, error)
	Attach(ctx context.Context, req AttachRequest) (AttachResult, error)
	Land(ctx context.Context, req LandRequest) (LandResult, error)
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
	Email string `json:"email"`
	Team  string `json:"team"`
}

type TeamOptions struct {
	Endpoint     string
	Lane         string
	DefaultModel string
	AccessToken  string
}

type Team struct {
	Name         string   `json:"name"`
	Endpoint     string   `json:"endpoint"`
	Lane         string   `json:"lane"`
	Boxes        []string `json:"boxes"`
	DefaultModel string   `json:"defaultModel"`
	Models       []string `json:"models"`
}

type Session struct {
	ID       string `json:"id"`
	State    string `json:"state"`
	Harness  string `json:"harness"`
	Location string `json:"location"`
	Lane     string `json:"lane"`
	Model    string `json:"model"`
	Age      string `json:"age"`
	Spent    string `json:"spent"`
	Task     string `json:"task,omitempty"`
}

type DetachRequest struct {
	SessionID   string `json:"sessionId"`
	Harness     string `json:"harness"`
	Box         string `json:"box"`
	Team        Team   `json:"team"`
	AccessToken string `json:"-"`
}

type ResumeRequest struct {
	SessionID   string  `json:"sessionId"`
	Session     Session `json:"session"`
	Team        Team    `json:"team"`
	AccessToken string  `json:"-"`
}

type AttachRequest struct {
	SessionID   string `json:"sessionId"`
	Team        Team   `json:"team"`
	AccessToken string `json:"-"`
}

type AttachResult struct {
	SessionID string `json:"sessionId"`
	URL       string `json:"url"`
	Protocol  string `json:"protocol"`
}

type LandRequest struct {
	SessionID   string  `json:"sessionId"`
	Session     Session `json:"session"`
	Team        Team    `json:"team"`
	Title       string  `json:"title"`
	AccessToken string  `json:"-"`
}

type LandResult struct {
	ID      string       `json:"id"`
	Title   string       `json:"title"`
	Branch  string       `json:"branch"`
	RanOn   []string     `json:"ranOn"`
	Harness string       `json:"harness"`
	Model   string       `json:"model"`
	Cost    string       `json:"cost"`
	Review  ReviewPacket `json:"review"`
}

type ReviewPacket struct {
	Summary           string   `json:"summary"`
	Checklist         []string `json:"checklist"`
	SuggestedCommands []string `json:"suggestedCommands"`
}
