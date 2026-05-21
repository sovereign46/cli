package share

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	DefaultAPIBaseURL = "https://gist.s46.dev"
	DefaultViewerURL  = "https://share.s46.dev"
	DefaultTTL        = "30d"
	BlobContentType   = "application/vnd.s46.share+json"
)

var ValidTTLs = map[string]bool{"1d": true, "7d": true, "30d": true, "365d": true, "never": true}

type Client struct {
	BaseURL     string
	UploadToken string
	HTTPClient  *http.Client
}

type UploadRequest struct {
	ID          string `json:"id,omitempty"`
	Blob        string `json:"blob"`
	TTL         string `json:"ttl"`
	ContentType string `json:"contentType"`
	RevokeKey   string `json:"revokeKey,omitempty"`
}

type UploadResponse struct {
	ID        string `json:"id"`
	URL       string `json:"url"`
	TTL       string `json:"ttl"`
	ExpiresAt string `json:"expiresAt"`
	RevokeKey string `json:"revokeKey,omitempty"`
}

type DeleteResponse struct {
	ID      string `json:"id"`
	Deleted bool   `json:"deleted"`
}

func NormalizeTTL(ttl string) (string, error) {
	if strings.TrimSpace(ttl) == "" {
		return DefaultTTL, nil
	}
	ttl = strings.ToLower(strings.TrimSpace(ttl))
	if !ValidTTLs[ttl] {
		return "", fmt.Errorf("invalid share ttl %q; expected one of 1d, 7d, 30d, 365d, never", ttl)
	}
	return ttl, nil
}

func (c Client) Create(ctx context.Context, req UploadRequest) (UploadResponse, error) {
	var out UploadResponse
	if err := c.doJSON(ctx, http.MethodPost, "/v1/shares", req, &out, ""); err != nil {
		return UploadResponse{}, err
	}
	return out, nil
}

func (c Client) Update(ctx context.Context, id string, req UploadRequest) (UploadResponse, error) {
	var out UploadResponse
	if err := c.doJSON(ctx, http.MethodPut, "/v1/shares/"+id, req, &out, ""); err != nil {
		return UploadResponse{}, err
	}
	return out, nil
}

func (c Client) Delete(ctx context.Context, id string, revokeKey string) (DeleteResponse, error) {
	var out DeleteResponse
	if err := c.doJSON(ctx, http.MethodDelete, "/v1/shares/"+id, nil, &out, revokeKey); err != nil {
		return DeleteResponse{}, err
	}
	return out, nil
}

func (c Client) doJSON(ctx context.Context, method string, path string, body any, out any, revokeKey string) error {
	baseURL := strings.TrimRight(c.BaseURL, "/")
	if baseURL == "" {
		baseURL = DefaultAPIBaseURL
	}
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.UploadToken != "" && method != http.MethodDelete {
		req.Header.Set("Authorization", "Bearer "+c.UploadToken)
	}
	if revokeKey != "" {
		req.Header.Set("X-S46-Revoke-Key", revokeKey)
	}
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("s46-gist %s %s failed: HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(responseBody)))
	}
	if out == nil || len(responseBody) == 0 {
		return nil
	}
	return json.Unmarshal(responseBody, out)
}
