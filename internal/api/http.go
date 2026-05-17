package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const DefaultHTTPTimeout = 30 * time.Second

type HTTPClient struct {
	BaseURL string
	Client  *http.Client
	Timeout time.Duration
}

func NewHTTPClient(baseURL string) *HTTPClient {
	return &HTTPClient{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Client:  &http.Client{Timeout: DefaultHTTPTimeout},
		Timeout: DefaultHTTPTimeout,
	}
}

func (c *HTTPClient) StartDeviceLogin(ctx context.Context) (DeviceLogin, error) {
	var response struct {
		DeviceCode      string `json:"deviceCode"`
		UserCode        string `json:"userCode"`
		VerificationURI string `json:"verificationUri"`
		IntervalSeconds int    `json:"intervalSeconds"`
		ExpiresAt       string `json:"expiresAt"`
	}
	if err := c.do(ctx, http.MethodPost, "/v1/auth/device/start", "", nil, &response); err != nil {
		return DeviceLogin{}, err
	}
	expiresAt, _ := time.Parse(time.RFC3339, response.ExpiresAt)
	return DeviceLogin{
		DeviceCode:      response.DeviceCode,
		UserCode:        response.UserCode,
		VerificationURI: c.verificationURIForDisplay(response.VerificationURI),
		Interval:        time.Duration(response.IntervalSeconds) * time.Second,
		ExpiresAt:       expiresAt,
	}, nil
}

func (c *HTTPClient) verificationURIForDisplay(raw string) string {
	if raw == "" {
		return raw
	}
	base, err := url.Parse(c.BaseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return raw
	}
	verification, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if !verification.IsAbs() {
		return verificationURIOnBaseOrigin(*base, *verification)
	}
	if !isLocalDevelopmentHost(base.Hostname()) || !isS46Host(verification.Hostname()) {
		return raw
	}
	verification.Scheme = base.Scheme
	verification.Host = base.Host
	return verification.String()
}

func verificationURIOnBaseOrigin(base url.URL, verification url.URL) string {
	base.Path = verification.Path
	base.RawPath = verification.RawPath
	base.RawQuery = verification.RawQuery
	base.Fragment = verification.Fragment
	return base.String()
}

func isLocalDevelopmentHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified())
}

func isS46Host(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	return host == "s46.dev" || strings.HasSuffix(host, ".s46.dev")
}

func (c *HTTPClient) PollDeviceLogin(ctx context.Context, deviceCode string, userHint string) (TokenSet, error) {
	var tokens TokenSet
	err := c.do(ctx, http.MethodPost, "/v1/auth/device/poll", "", map[string]string{"deviceCode": deviceCode, "userHint": userHint}, &tokens)
	return tokens, err
}

func (c *HTTPClient) RefreshToken(ctx context.Context, refreshToken string, account string) (TokenSet, error) {
	var tokens TokenSet
	err := c.do(ctx, http.MethodPost, "/v1/auth/token/refresh", "", map[string]string{"refreshToken": refreshToken, "account": account}, &tokens)
	return tokens, err
}

func (c *HTTPClient) Me(ctx context.Context, accessToken string) (User, error) {
	var user User
	err := c.do(ctx, http.MethodGet, "/v1/me", accessToken, nil, &user)
	return user, err
}

func (c *HTTPClient) Team(ctx context.Context, name string, opts TeamOptions) (Team, error) {
	query := url.Values{}
	if opts.Endpoint != "" {
		query.Set("endpoint", opts.Endpoint)
	}
	if opts.Lane != "" {
		query.Set("lane", opts.Lane)
	}
	if opts.Mode != "" {
		query.Set("mode", opts.Mode)
	}
	if opts.DefaultModel != "" {
		query.Set("defaultModel", opts.DefaultModel)
	}
	endpoint := "/v1/teams/" + url.PathEscape(name)
	if encoded := query.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}
	var team Team
	err := c.do(ctx, http.MethodGet, endpoint, "", nil, &team)
	return team, err
}

func (c *HTTPClient) Sessions(ctx context.Context, team Team) ([]Session, error) {
	var response struct {
		Sessions []Session `json:"sessions"`
	}
	err := c.do(ctx, http.MethodGet, "/v1/sessions", "", nil, &response)
	return response.Sessions, err
}

func (c *HTTPClient) Detach(ctx context.Context, req DetachRequest) (Session, error) {
	var session Session
	err := c.do(ctx, http.MethodPost, "/v1/sessions/"+url.PathEscape(req.SessionID)+"/detach", "", req, &session)
	return session, err
}

func (c *HTTPClient) Resume(ctx context.Context, req ResumeRequest) (Session, error) {
	var session Session
	err := c.do(ctx, http.MethodPost, "/v1/sessions/"+url.PathEscape(req.SessionID)+"/resume", "", req, &session)
	return session, err
}

func (c *HTTPClient) Attach(ctx context.Context, req AttachRequest) (AttachResult, error) {
	var result AttachResult
	err := c.do(ctx, http.MethodPost, "/v1/sessions/"+url.PathEscape(req.SessionID)+"/attach", "", req, &result)
	return result, err
}

func (c *HTTPClient) Land(ctx context.Context, req LandRequest) (LandResult, error) {
	var result LandResult
	err := c.do(ctx, http.MethodPost, "/v1/sessions/"+url.PathEscape(req.SessionID)+"/land", "", req, &result)
	return result, err
}

func (c *HTTPClient) do(ctx context.Context, method string, endpoint string, bearer string, body any, target any) error {
	if _, ok := ctx.Deadline(); !ok {
		timeout := c.Timeout
		if timeout == 0 {
			timeout = DefaultHTTPTimeout
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.BaseURL+endpoint, reader)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: DefaultHTTPTimeout}
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %s", method, endpoint, response.Status)
	}
	if target == nil {
		return nil
	}
	return json.NewDecoder(response.Body).Decode(target)
}
