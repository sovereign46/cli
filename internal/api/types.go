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

type Client interface {
	StartDeviceLogin(ctx context.Context) (DeviceLogin, error)
	PollDeviceLogin(ctx context.Context, deviceCode string, userHint string) (TokenSet, error)
	RefreshToken(ctx context.Context, refreshToken string, account string) (TokenSet, error)
	Me(ctx context.Context, accessToken string) (User, error)
	Team(ctx context.Context, name string, opts TeamOptions) (Team, error)
	Sessions(ctx context.Context, team Team) ([]Session, error)
	Detach(ctx context.Context, req DetachRequest) (Session, error)
	Resume(ctx context.Context, req ResumeRequest) (Session, error)
	Land(ctx context.Context, req LandRequest) (LandResult, error)
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
	AccessToken  string    `json:"accessToken"`
	RefreshToken string    `json:"refreshToken"`
	ExpiresAt    time.Time `json:"expiresAt"`
}

type User struct {
	Email string `json:"email"`
	Team  string `json:"team"`
}

type TeamOptions struct {
	Endpoint     string
	Lane         string
	Mode         string
	DefaultModel string
}

type Team struct {
	Name         string   `json:"name"`
	Endpoint     string   `json:"endpoint"`
	Lane         string   `json:"lane"`
	Mode         string   `json:"mode"`
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
	SessionID string `json:"sessionId"`
	Harness   string `json:"harness"`
	Box       string `json:"box"`
	Team      Team   `json:"team"`
}

type ResumeRequest struct {
	SessionID string  `json:"sessionId"`
	Session   Session `json:"session"`
}

type LandRequest struct {
	SessionID string  `json:"sessionId"`
	Session   Session `json:"session"`
	Team      Team    `json:"team"`
	Title     string  `json:"title"`
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
