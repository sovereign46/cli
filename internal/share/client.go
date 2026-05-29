package share

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/bits"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sovereign46/cli/internal/contextx"
)

const (
	DefaultAPIBaseURL    = "https://gist.s46.dev"
	DefaultViewerURL     = "https://share.s46.dev"
	DefaultTTL           = "30d"
	BlobContentType      = "application/vnd.s46.share+json"
	DefaultHTTPTimeout   = 20 * time.Second
	maxResponseBytes     = 1 << 20
	maxErrorSnippetBytes = 4 * 1024
	userAgent            = "s46-cli"

	MaxPOWDifficulty = 26
)

var (
	validTTLs           = []string{"1d", "7d", "30d", "365d", "never"}
	validTTLDescription = strings.Join(validTTLs, ", ")
)

type Client struct {
	BaseURL           string
	AnonymousClientID string
	HTTPClient        *http.Client
}

type UploadRequest struct {
	ID          string `json:"id,omitempty"`
	Blob        string `json:"blob"`
	TTL         string `json:"ttl"`
	ContentType string `json:"contentType"`
	RevokeKey   string `json:"revokeKey,omitempty"`
}

type UploadResponse struct {
	ID           string `json:"id"`
	URL          string `json:"url"`
	TTL          string `json:"ttl"`
	ExpiresAt    string `json:"expiresAt"`
	RevokeKey    string `json:"revokeKey,omitempty"`
	Deduplicated bool   `json:"deduplicated,omitempty"`
}

type DeleteResponse struct {
	ID      string `json:"id"`
	Deleted bool   `json:"deleted"`
}

type ChallengeRequest struct {
	ClientID  string `json:"clientId"`
	BodyHash  string `json:"bodyHash"`
	Operation string `json:"operation"`
}

type ChallengeResponse struct {
	Algorithm  string `json:"algorithm"`
	Nonce      string `json:"nonce"`
	ExpiresAt  string `json:"expiresAt"`
	Difficulty int    `json:"difficulty"`
	BodyHash   string `json:"bodyHash"`
	ClientID   string `json:"clientId"`
	Operation  string `json:"operation"`
	Challenge  string `json:"challenge"`
}

func NormalizeTTL(ttl string) (string, error) {
	if strings.TrimSpace(ttl) == "" {
		return DefaultTTL, nil
	}
	ttl = strings.ToLower(strings.TrimSpace(ttl))
	if !slices.Contains(validTTLs, ttl) {
		return "", fmt.Errorf("invalid share ttl %q; expected one of %s", ttl, validTTLDescription)
	}
	return ttl, nil
}

func (c Client) Create(ctx context.Context, req UploadRequest) (UploadResponse, error) {
	var out UploadResponse
	if err := c.doJSON(ctx, http.MethodPost, "/v1/shares", req, &out, "", "create"); err != nil {
		return UploadResponse{}, err
	}
	return out, nil
}

func (c Client) Update(ctx context.Context, id string, req UploadRequest) (UploadResponse, error) {
	var out UploadResponse
	if err := c.doJSON(ctx, http.MethodPut, "/v1/shares/"+url.PathEscape(id), req, &out, "", "update"); err != nil {
		return UploadResponse{}, err
	}
	return out, nil
}

func (c Client) Delete(ctx context.Context, id string, revokeKey string) (DeleteResponse, error) {
	var out DeleteResponse
	if err := c.doJSON(ctx, http.MethodDelete, "/v1/shares/"+url.PathEscape(id), nil, &out, revokeKey, ""); err != nil {
		return DeleteResponse{}, err
	}
	return out, nil
}

func (c Client) doJSON(ctx context.Context, method string, path string, body any, out any, revokeKey string, proofOperation string) error {
	httpClient, timeout := c.httpClient()
	ctx, cancel := contextx.WithMaxTimeout(ctx, timeout)
	defer cancel()

	var payload []byte
	var reader io.Reader
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode s46-gist %s %s request: %w", method, path, err)
		}
		reader = bytes.NewReader(payload)
	}

	proof, err := c.proofHeaders(ctx, proofOperation, payload)
	if err != nil {
		return fmt.Errorf("prepare s46-gist proof for %s %s: %w", method, path, err)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL()+path, reader)
	if err != nil {
		return fmt.Errorf("build s46-gist %s %s request: %w", method, path, err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for key, value := range proof {
		req.Header.Set(key, value)
	}
	if revokeKey != "" {
		req.Header.Set("X-S46-Revoke-Key", revokeKey)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		if ctxErr := contextx.Done(req.Context(), err); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("s46-gist %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("read s46-gist %s %s response: %w", method, path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("s46-gist %s %s failed: HTTP %d: %s", method, path, resp.StatusCode, responseSnippet(responseBody))
	}
	if out == nil || len(responseBody) == 0 {
		return nil
	}
	if err := json.Unmarshal(responseBody, out); err != nil {
		return fmt.Errorf("decode s46-gist %s %s response: %w", method, path, err)
	}
	return nil
}

func (c Client) proofHeaders(ctx context.Context, operation string, payload []byte) (map[string]string, error) {
	if operation == "" {
		return nil, nil
	}
	clientID := strings.TrimSpace(c.AnonymousClientID)
	if clientID == "" {
		return nil, fmt.Errorf("missing anonymous share client id")
	}
	bodyHash := sha256Hex(payload)
	challenge, err := c.requestChallenge(ctx, ChallengeRequest{ClientID: clientID, BodyHash: bodyHash, Operation: operation})
	if err != nil {
		return nil, fmt.Errorf("request share proof challenge: %w", err)
	}
	if challenge.Algorithm != "" && challenge.Algorithm != "sha256" {
		return nil, fmt.Errorf("unsupported share proof algorithm %q", challenge.Algorithm)
	}
	if challenge.BodyHash != "" && challenge.BodyHash != bodyHash {
		return nil, fmt.Errorf("share proof challenge body hash mismatch")
	}
	if challenge.Nonce == "" || challenge.Challenge == "" {
		return nil, fmt.Errorf("share proof challenge is missing required fields")
	}
	suffix, err := solveProof(ctx, challenge.Nonce, bodyHash, challenge.Difficulty)
	if err != nil {
		return nil, fmt.Errorf("solve share proof challenge: %w", err)
	}
	return map[string]string{
		"X-S46-Client-ID":     clientID,
		"X-S46-POW-Challenge": challenge.Challenge,
		"X-S46-POW-Suffix":    suffix,
	}, nil
}

func (c Client) requestChallenge(ctx context.Context, body ChallengeRequest) (ChallengeResponse, error) {
	httpClient, timeout := c.httpClient()
	ctx, cancel := contextx.WithMaxTimeout(ctx, timeout)
	defer cancel()

	payload, err := json.Marshal(body)
	if err != nil {
		return ChallengeResponse{}, fmt.Errorf("encode s46-gist challenge request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL()+"/v1/share-challenges", bytes.NewReader(payload))
	if err != nil {
		return ChallengeResponse{}, fmt.Errorf("build s46-gist challenge request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("X-S46-Client-ID", body.ClientID)

	resp, err := httpClient.Do(req)
	if err != nil {
		if ctxErr := contextx.Done(req.Context(), err); ctxErr != nil {
			return ChallengeResponse{}, ctxErr
		}
		return ChallengeResponse{}, fmt.Errorf("s46-gist POST /v1/share-challenges: %w", err)
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return ChallengeResponse{}, fmt.Errorf("read s46-gist challenge response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ChallengeResponse{}, fmt.Errorf("s46-gist challenge failed: HTTP %d: %s", resp.StatusCode, responseSnippet(responseBody))
	}
	var out ChallengeResponse
	if err := json.Unmarshal(responseBody, &out); err != nil {
		return ChallengeResponse{}, fmt.Errorf("decode s46-gist challenge response: %w", err)
	}
	return out, nil
}

func responseSnippet(body []byte) string {
	if len(body) > maxErrorSnippetBytes {
		body = body[:maxErrorSnippetBytes]
	}
	return strings.TrimSpace(string(body))
}

func (c Client) baseURL() string {
	baseURL := strings.TrimRight(c.BaseURL, "/")
	if baseURL == "" {
		baseURL = DefaultAPIBaseURL
	}
	return baseURL
}

func (c Client) httpClient() (*http.Client, time.Duration) {
	return contextx.HTTPClientTimeout(c.HTTPClient, DefaultHTTPTimeout)
}

func solveProof(ctx context.Context, nonce string, bodyHash string, difficulty int) (string, error) {
	if difficulty < 0 || difficulty > MaxPOWDifficulty {
		return "", fmt.Errorf("share proof difficulty %d is outside supported range 0-%d", difficulty, MaxPOWDifficulty)
	}
	for counter := uint64(0); ; counter++ {
		if counter%4096 == 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			default:
			}
		}
		suffix := strconv.FormatUint(counter, 36)
		sum := sha256.Sum256([]byte(nonce + bodyHash + suffix))
		if leadingZeroBits(sum[:]) >= difficulty {
			return suffix, nil
		}
	}
}

func leadingZeroBits(value []byte) int {
	count := 0
	for _, b := range value {
		if b == 0 {
			count += 8
			continue
		}
		return count + bits.LeadingZeros8(b)
	}
	return count
}

func sha256Hex(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
