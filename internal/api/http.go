package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sovereign46/cli/internal/contextx"
)

const DefaultHTTPTimeout = 30 * time.Second

var (
	ErrAuthorizationPending = errors.New("authorization pending")
	ErrExpired              = errors.New("expired")
	ErrNotInvited           = errors.New("not invited")
	ErrAuthenticateFirst    = errors.New("authenticate first")
	ErrUnauthorized         = errors.New("unauthorized")
	ErrForbidden            = errors.New("forbidden")
	// ErrCloudUnavailable wraps any transport-level failure when calling
	// the s46 API: DNS failure, connection refused, timeout, network
	// unreachable, TLS handshake errors, etc. Callers use errors.Is to
	// branch on "the API isn't reachable" without grepping error text.
	ErrCloudUnavailable = errors.New("cloud unavailable")
)

type Error struct {
	Code       string
	Message    string
	StatusCode int
	Err        error
}

func (e Error) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Code != "" {
		return e.Code
	}
	return fmt.Sprintf("HTTP %d", e.StatusCode)
}

func (e Error) Unwrap() error {
	return e.Err
}

func (e Error) ErrorCode() string {
	return e.Code
}

type HTTPClient struct {
	BaseURL string
	Client  *http.Client
	Timeout time.Duration
}

func NewHTTPClient(baseURL string) *HTTPClient {
	return &HTTPClient{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Client:  &http.Client{},
		Timeout: DefaultHTTPTimeout,
	}
}

func (c *HTTPClient) StartDeviceLogin(ctx context.Context, req DeviceLoginRequest) (DeviceLogin, error) {
	var response struct {
		DeviceCode      string `json:"deviceCode"`
		UserCode        string `json:"userCode"`
		VerificationURI string `json:"verificationUri"`
		IntervalSeconds int    `json:"intervalSeconds"`
		ExpiresAt       string `json:"expiresAt"`
	}
	if err := c.do(ctx, http.MethodPost, "/v1/auth/device/start", "", req, &response); err != nil {
		return DeviceLogin{}, err
	}
	var expiresAt time.Time
	if response.ExpiresAt != "" {
		var err error
		expiresAt, err = time.Parse(time.RFC3339, response.ExpiresAt)
		if err != nil {
			return DeviceLogin{}, fmt.Errorf("parse device login expiry %q: %w", response.ExpiresAt, err)
		}
	}
	return DeviceLogin{
		DeviceCode:      response.DeviceCode,
		UserCode:        response.UserCode,
		VerificationURI: c.rewriteS46URL(response.VerificationURI),
		Interval:        time.Duration(response.IntervalSeconds) * time.Second,
		ExpiresAt:       expiresAt,
	}, nil
}

func (c *HTTPClient) PollDeviceLogin(ctx context.Context, deviceCode string) (TokenSet, error) {
	var tokens TokenSet
	err := c.do(ctx, http.MethodPost, "/v1/auth/device/poll", "", map[string]string{"deviceCode": deviceCode}, &tokens)
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

func (c *HTTPClient) Devices(ctx context.Context, accessToken string) ([]Device, error) {
	var response struct {
		Devices []Device `json:"devices"`
	}
	err := c.do(ctx, http.MethodGet, "/v1/devices", accessToken, nil, &response)
	return response.Devices, err
}

func (c *HTTPClient) DeleteDevice(ctx context.Context, deviceID string, accessToken string) error {
	return c.do(ctx, http.MethodDelete, "/v1/devices/"+url.PathEscape(deviceID), accessToken, nil, nil)
}

func (c *HTTPClient) Team(ctx context.Context, name string, opts TeamOptions) (Team, error) {
	query := url.Values{}
	if opts.Endpoint != "" {
		query.Set("endpoint", opts.Endpoint)
	}
	if opts.Region != "" {
		query.Set("region", opts.Region)
	}
	if opts.DefaultModel != "" {
		query.Set("defaultModel", opts.DefaultModel)
	}
	endpoint := "/v1/teams/" + url.PathEscape(name)
	if encoded := query.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}
	var team Team
	err := c.do(ctx, http.MethodGet, endpoint, opts.AccessToken, nil, &team)
	team.Endpoint = c.rewriteS46URL(team.Endpoint)
	for i := range team.WorkerHosts {
		team.WorkerHosts[i] = c.rewriteS46Location(team.WorkerHosts[i])
	}
	return team, err
}

func (c *HTTPClient) Sessions(ctx context.Context, team Team, accessToken string) ([]Session, error) {
	var response struct {
		Sessions []Session `json:"sessions"`
	}
	err := c.do(ctx, http.MethodGet, sessionListEndpoint(team.Name), accessToken, nil, &response)
	for i := range response.Sessions {
		c.normalizeSession(&response.Sessions[i])
	}
	return response.Sessions, err
}

func (c *HTTPClient) Detach(ctx context.Context, req DetachRequest) (Session, error) {
	var session Session
	err := c.do(ctx, http.MethodPost, sessionActionEndpoint(req.SessionID, "detach", req.Team.Name), req.AccessToken, req, &session)
	c.normalizeSession(&session)
	return session, err
}

func (c *HTTPClient) Resume(ctx context.Context, req ResumeRequest) (Session, error) {
	var session Session
	err := c.do(ctx, http.MethodPost, sessionActionEndpoint(req.SessionID, "resume", req.Team.Name), req.AccessToken, req, &session)
	c.normalizeSession(&session)
	return session, err
}

func (c *HTTPClient) Attach(ctx context.Context, req AttachRequest) (AttachResult, error) {
	var result AttachResult
	err := c.do(ctx, http.MethodPost, sessionActionEndpoint(req.SessionID, "attach", req.Team.Name), req.AccessToken, req, &result)
	result.URL = c.rewriteS46URL(result.URL)
	return result, err
}

func (c *HTTPClient) Land(ctx context.Context, req LandRequest) (LandResult, error) {
	var result LandResult
	endpoint := addTeamQuery("/v1/sessions/"+url.PathEscape(req.SessionID)+"/land", req.Team.Name)
	err := c.do(ctx, http.MethodPost, endpoint, req.AccessToken, req, &result)
	for i := range result.RanOn {
		result.RanOn[i] = c.rewriteS46Location(result.RanOn[i])
	}
	return result, err
}

func sessionListEndpoint(teamName string) string {
	return addTeamQuery("/v1/sessions", teamName)
}

func sessionActionEndpoint(sessionID string, action string, teamName string) string {
	endpoint := "/v1/sessions/" + url.PathEscape(sessionID) + "/" + action
	return addTeamQuery(endpoint, teamName)
}

func addTeamQuery(endpoint string, teamName string) string {
	if teamName == "" {
		return endpoint
	}
	query := url.Values{}
	query.Set("team", teamName)
	return endpoint + "?" + query.Encode()
}

func (c *HTTPClient) normalizeSession(session *Session) {
	session.Location = c.rewriteS46Location(session.Location)
}

func (c *HTTPClient) rewriteS46URL(raw string) string {
	if raw == "" {
		return raw
	}
	base, baseOK := c.displayBaseURL()
	if !baseOK {
		return raw
	}
	target, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if !target.IsAbs() {
		resolved := urlOnBaseOrigin(base, *target)
		return resolved.String()
	}
	if !isLocalDevelopmentHost(base.Hostname()) || !isS46Host(target.Hostname()) {
		return raw
	}
	target.Scheme = displayScheme(target.Scheme, base.Scheme)
	target.Host = base.Host
	return target.String()
}

func (c *HTTPClient) rewriteS46Location(raw string) string {
	if raw == "" {
		return raw
	}
	if strings.Contains(raw, "://") {
		return c.rewriteS46URL(raw)
	}
	base, ok := c.localDisplayBaseURL()
	if !ok {
		return raw
	}
	host, suffix, ok := splitHostSuffix(raw)
	if !ok || !isS46Host(host) {
		return raw
	}
	return base.Host + suffix
}

func (c *HTTPClient) displayBaseURL() (url.URL, bool) {
	base, err := url.Parse(c.BaseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return url.URL{}, false
	}
	return *base, true
}

func (c *HTTPClient) localDisplayBaseURL() (url.URL, bool) {
	base, ok := c.displayBaseURL()
	if !ok || !isLocalDevelopmentHost(base.Hostname()) {
		return url.URL{}, false
	}
	return base, true
}

func urlOnBaseOrigin(base url.URL, target url.URL) url.URL {
	base.Path = target.Path
	base.RawPath = target.RawPath
	base.RawQuery = target.RawQuery
	base.Fragment = target.Fragment
	return base
}

func displayScheme(targetScheme string, baseScheme string) string {
	switch targetScheme {
	case "ws", "wss":
		if baseScheme == "https" {
			return "wss"
		}
		return "ws"
	default:
		return baseScheme
	}
}

func splitHostSuffix(value string) (string, string, bool) {
	hostWithPort, suffix, _ := strings.Cut(value, "/")
	if suffix != "" {
		suffix = "/" + suffix
	}
	host := hostWithPort
	if splitHost, _, err := net.SplitHostPort(hostWithPort); err == nil {
		host = splitHost
	}
	return host, suffix, host != ""
}

func LocalDevelopmentOrigin(baseURL string) (string, bool) {
	base, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || base.Scheme == "" || base.Host == "" || !isLocalDevelopmentHost(base.Hostname()) {
		return "", false
	}
	return base.Scheme + "://" + base.Host, true
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

func (c *HTTPClient) do(ctx context.Context, method string, endpoint string, bearer string, body any, target any) error {
	parentCtx := ctx
	timeout := c.Timeout
	if timeout == 0 {
		timeout = DefaultHTTPTimeout
	}
	ctx, cancel := contextx.WithMaxTimeout(parentCtx, timeout)
	defer cancel()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode %s %s request: %w", method, endpoint, err)
		}
		reader = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.BaseURL+endpoint, reader)
	if err != nil {
		return fmt.Errorf("build %s %s request: %w", method, endpoint, err)
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	client := contextx.WithoutHTTPTimeout(c.Client)
	response, err := client.Do(request)
	if err != nil {
		if ctxErr := contextx.Done(parentCtx, err); ctxErr != nil {
			return ctxErr
		}
		// Transport failure: classify as ErrCloudUnavailable so callers
		// can branch via errors.Is without inspecting message text.
		return fmt.Errorf("%w: %s %s: %w", ErrCloudUnavailable, method, endpoint, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return decodeErrorResponse(method, endpoint, response)
	}
	if target == nil {
		return nil
	}
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		return fmt.Errorf("decode %s %s response: %w", method, endpoint, err)
	}
	return nil
}

func decodeErrorResponse(method string, endpoint string, response *http.Response) error {
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read %s %s error response: %w", method, endpoint, err)
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		// Non-JSON error bodies fall through to the raw response text below.
		body.Error.Code = ""
		body.Error.Message = ""
	}
	apiErr := Error{Code: body.Error.Code, Message: body.Error.Message, StatusCode: response.StatusCode, Err: sentinelForCode(body.Error.Code)}
	if apiErr.Code != "" || apiErr.Message != "" {
		return apiErr
	}
	message := strings.TrimSpace(string(raw))
	if message != "" {
		return fmt.Errorf("%s %s: %s: %s", method, endpoint, response.Status, message)
	}
	return fmt.Errorf("%s %s: %s", method, endpoint, response.Status)
}

func sentinelForCode(code string) error {
	switch code {
	case "authorization_pending":
		return ErrAuthorizationPending
	case "expired":
		return ErrExpired
	case "not_invited":
		return ErrNotInvited
	case "authenticate_first":
		return ErrAuthenticateFirst
	case "unauthorized":
		return ErrUnauthorized
	case "forbidden":
		return ErrForbidden
	default:
		return nil
	}
}
