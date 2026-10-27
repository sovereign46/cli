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
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sovereign46/cli/internal/contextx"
)

const (
	DefaultHTTPTimeout   = 30 * time.Second
	maxResponseBytes     = 1 << 20
	maxErrorMessageBytes = 4 * 1024
	maxResponseJSONDepth = 128
	userAgent            = "s46-cli"
)

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
	now     func() time.Time
}

func NewHTTPClient(baseURL string) *HTTPClient {
	return &HTTPClient{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Client:  &http.Client{},
		Timeout: DefaultHTTPTimeout,
		now:     time.Now,
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
	headers, err := c.doWithHeaders(ctx, http.MethodGet, endpoint, opts.AccessToken, nil, &team)
	if err != nil {
		return Team{}, err
	}
	if err := ValidateTeamReleaseMetadata(team, c.now().UTC()); err != nil {
		return Team{}, fmt.Errorf("validate team release metadata: %w", err)
	}
	if err := validateTeamReleaseHeaders(team, headers); err != nil {
		return Team{}, fmt.Errorf("validate team release headers: %w", err)
	}
	team.Endpoint = c.rewriteS46URL(team.Endpoint)
	for i := range team.WorkerHosts {
		team.WorkerHosts[i] = c.rewriteS46Location(team.WorkerHosts[i])
	}
	return team, nil
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

func sessionListEndpoint(teamName string) string {
	return addTeamQuery("/v1/sessions", teamName)
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
	_, err := c.doWithHeaders(ctx, method, endpoint, bearer, body, target)
	return err
}

func (c *HTTPClient) doWithHeaders(ctx context.Context, method string, endpoint string, bearer string, body any, target any) (http.Header, error) {
	timeout := c.Timeout
	if timeout == 0 {
		timeout = DefaultHTTPTimeout
	}
	client, timeout := contextx.HTTPClientTimeout(c.Client, timeout)
	ctx, cancel := contextx.WithMaxTimeout(ctx, timeout)
	defer cancel()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode %s %s request: %w", method, endpoint, err)
		}
		reader = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.BaseURL+endpoint, reader)
	if err != nil {
		return nil, fmt.Errorf("build %s %s request: %w", method, endpoint, err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", userAgent)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	response, err := client.Do(request)
	if err != nil {
		if ctxErr := contextx.Done(request.Context(), err); ctxErr != nil {
			return nil, ctxErr
		}
		// Transport failure: classify as ErrCloudUnavailable so callers
		// can branch via errors.Is without inspecting message text.
		return nil, fmt.Errorf("%w: %s %s: %w", ErrCloudUnavailable, method, endpoint, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, decodeErrorResponse(method, endpoint, response)
	}
	if target == nil {
		return response.Header.Clone(), nil
	}
	if err := decodeSuccessResponse(response.Body, target); err != nil {
		return nil, fmt.Errorf("decode %s %s response: %w", method, endpoint, err)
	}
	return response.Header.Clone(), nil
}

func validateTeamReleaseHeaders(team Team, headers http.Header) error {
	expected := map[string]string{
		"X-S46-Release-Channel":      PrelaunchReleaseChannel,
		"X-S46-Production-Certified": "false",
		"X-S46-Production-API-Path":  "false",
	}
	hasHeaders := false
	for name, value := range expected {
		values := headers.Values(name)
		if len(values) > 0 {
			hasHeaders = true
		}
		if len(values) > 0 && (len(values) != 1 || values[0] != value) {
			return fmt.Errorf("response header %s must occur exactly once with value %q", name, value)
		}
	}
	if team.ReleaseChannel == "" && !hasHeaders {
		return nil
	}
	if team.ReleaseChannel == "" || !hasHeaders {
		return errors.New("prelaunch response headers and body classification disagree")
	}
	for name, value := range expected {
		values := headers.Values(name)
		if len(values) != 1 || values[0] != value {
			return fmt.Errorf("prelaunch response header %s must occur exactly once with value %q", name, value)
		}
	}
	if team.ReleaseChannel != PrelaunchReleaseChannel || team.ProductionCertified == nil || *team.ProductionCertified || team.ProductionAPIPath == nil || *team.ProductionAPIPath {
		return errors.New("prelaunch response headers and body classification disagree")
	}
	return nil
}

func decodeSuccessResponse(body io.Reader, target any) error {
	raw, err := io.ReadAll(io.LimitReader(body, maxResponseBytes+1))
	if err != nil {
		return err
	}
	if len(raw) > maxResponseBytes {
		return fmt.Errorf("response exceeds %d bytes", maxResponseBytes)
	}
	return UnmarshalStrictJSON(raw, target)
}

// UnmarshalStrictJSON preserves forward-compatible unknown fields while
// rejecting ambiguous JSON representations before encoding/json can normalize
// them into a known response field.
func UnmarshalStrictJSON(raw []byte, target any) error {
	if err := validateSingleJSONValue(raw, target); err != nil {
		return err
	}
	return json.Unmarshal(raw, target)
}

func validateSingleJSONValue(raw []byte, target any) error {
	if !utf8.Valid(raw) {
		return errors.New("response JSON is not valid UTF-8")
	}
	if err := validateJSONUnicodeEscapes(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := consumeJSONValue(decoder, 0, reflect.TypeOf(target)); err != nil {
		return err
	}
	if _, err := decoder.Token(); err == nil {
		return errors.New("response contains trailing JSON data")
	} else if !errors.Is(err, io.EOF) {
		return fmt.Errorf("response contains invalid trailing data: %w", err)
	}
	return nil
}

func consumeJSONValue(decoder *json.Decoder, depth int, expected reflect.Type) error {
	if depth > maxResponseJSONDepth {
		return fmt.Errorf("response JSON nesting exceeds %d", maxResponseJSONDepth)
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	expected = dereferenceJSONType(expected)
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		seenFolded := map[string]string{}
		fields := responseJSONFields(expected)
		var mapValue reflect.Type
		if expected != nil && expected.Kind() == reflect.Map && expected.Key().Kind() == reflect.String {
			mapValue = expected.Elem()
		}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object key is not a string")
			}
			if seen[key] {
				return fmt.Errorf("response contains duplicate JSON object key %q", key)
			}
			seen[key] = true
			folded := strings.ToLower(key)
			if prior, exists := seenFolded[folded]; exists && prior != key {
				return fmt.Errorf("response contains case-aliased JSON object keys %q and %q", prior, key)
			}
			seenFolded[folded] = key
			childType := mapValue
			if fields != nil {
				if fieldType, exists := fields[key]; exists {
					childType = fieldType
				} else if canonical := caseFoldedResponseField(fields, key); canonical != "" {
					return fmt.Errorf("response JSON field %q must use exact spelling %q", key, canonical)
				}
			}
			if err := consumeJSONValue(decoder, depth+1, childType); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim('}') {
			return errors.New("JSON object is not closed")
		}
	case '[':
		var elementType reflect.Type
		if expected != nil && (expected.Kind() == reflect.Slice || expected.Kind() == reflect.Array) {
			elementType = expected.Elem()
		}
		for decoder.More() {
			if err := consumeJSONValue(decoder, depth+1, elementType); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim(']') {
			return errors.New("JSON array is not closed")
		}
	default:
		return errors.New("JSON value starts with an unexpected delimiter")
	}
	return nil
}

func dereferenceJSONType(value reflect.Type) reflect.Type {
	for value != nil && value.Kind() == reflect.Pointer {
		value = value.Elem()
	}
	return value
}

func responseJSONFields(value reflect.Type) map[string]reflect.Type {
	if value == nil || value.Kind() != reflect.Struct || value == reflect.TypeOf(time.Time{}) {
		return nil
	}
	fields := make(map[string]reflect.Type, value.NumField())
	for index := 0; index < value.NumField(); index++ {
		field := value.Field(index)
		if !field.IsExported() {
			continue
		}
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name == "-" {
			continue
		}
		if name == "" {
			name = field.Name
		}
		fields[name] = field.Type
	}
	return fields
}

func caseFoldedResponseField(fields map[string]reflect.Type, key string) string {
	for canonical := range fields {
		if strings.EqualFold(canonical, key) {
			return canonical
		}
	}
	return ""
}

func validateJSONUnicodeEscapes(raw []byte) error {
	for index := 0; index < len(raw); index++ {
		if raw[index] != '\\' {
			continue
		}
		index++
		if index >= len(raw) || raw[index] != 'u' {
			continue
		}
		if index+4 >= len(raw) {
			return errors.New("response contains an incomplete JSON Unicode escape")
		}
		code, err := strconv.ParseUint(string(raw[index+1:index+5]), 16, 16)
		if err != nil {
			return fmt.Errorf("response contains an invalid JSON Unicode escape: %w", err)
		}
		index += 4
		if code >= 0xdc00 && code <= 0xdfff {
			return errors.New("response contains an unpaired JSON low surrogate")
		}
		if code >= 0xd800 && code <= 0xdbff {
			if index+6 >= len(raw) || raw[index+1] != '\\' ||
				raw[index+2] != 'u' {
				return errors.New("response contains an unpaired JSON high surrogate")
			}
			low, err := strconv.ParseUint(string(raw[index+3:index+7]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return errors.New("response contains an invalid JSON surrogate pair")
			}
			index += 6
		}
	}
	return nil
}

func decodeErrorResponse(method string, endpoint string, response *http.Response) error {
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("read %s %s error response: %w", method, endpoint, err)
	}
	if len(raw) > maxResponseBytes {
		return fmt.Errorf("%s %s error response exceeds %d bytes", method, endpoint, maxResponseBytes)
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := UnmarshalStrictJSON(raw, &body); err != nil {
		// Non-JSON and hostile JSON error bodies fall through to the raw
		// response text and must never derive a semantic auth sentinel.
		body.Error.Code = ""
		body.Error.Message = ""
	}
	apiErr := Error{Code: body.Error.Code, Message: body.Error.Message, StatusCode: response.StatusCode, Err: sentinelForCode(body.Error.Code)}
	if apiErr.Code != "" || apiErr.Message != "" {
		return apiErr
	}
	messageRaw := raw
	if len(messageRaw) > maxErrorMessageBytes {
		messageRaw = messageRaw[:maxErrorMessageBytes]
	}
	message := strings.TrimSpace(string(messageRaw))
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
