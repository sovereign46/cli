package api

import (
	"context"
	"time"
)

const (
	DefaultModel       = "s46/devstral-small-2-24b"
	DefaultTeam        = "@s46/engineering"
	DefaultAccount     = "dscape@s46.dev"
	DefaultGatewayURL  = "https://gateway.s46.dev"
	ResumeTargetLocal  = "local"
	ResumeTargetRemote = "remote"
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

type Team struct {
	Name         string   `json:"name"`
	Endpoint     string   `json:"endpoint"`
	Region       string   `json:"region"`
	WorkerHosts  []string `json:"workerHosts"`
	DefaultModel string   `json:"defaultModel"`
	Models       []string `json:"models"`
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

type DetachRequest struct {
	SessionID   string `json:"sessionId"`
	Harness     string `json:"harness"`
	Team        Team   `json:"team"`
	AccessToken string `json:"-"`
}

type ResumeRequest struct {
	SessionID   string  `json:"sessionId"`
	Session     Session `json:"session"`
	Team        Team    `json:"team"`
	Target      string  `json:"target,omitempty"`
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
	ID             string       `json:"id"`
	Title          string       `json:"title"`
	Branch         string       `json:"branch"`
	RanOn          []string     `json:"ranOn"`
	Harness        string       `json:"harness"`
	Model          string       `json:"model"`
	Cost           string       `json:"cost"`
	Status         string       `json:"status"`
	PullRequestURL string       `json:"pullRequestUrl,omitempty"`
	BlockedReason  string       `json:"blockedReason,omitempty"`
	Review         ReviewPacket `json:"review"`
}

type ReviewPacket struct {
	Summary           string   `json:"summary"`
	Checklist         []string `json:"checklist"`
	SuggestedCommands []string `json:"suggestedCommands"`
}
