# HTTP Layer

Use the standard library `net/http`. Put HTTP code in the package that talks to
the service. Build requests explicitly, pass contexts, and handle remote
failures near the call.

## Rules

- **Inject the client** - Accept an optional `*http.Client` for tests and custom transports. No mutable globals for clients, base URLs, tokens, or runtime config
- **Use request deadlines** - Prefer context deadlines over `http.Client.Timeout`. Normalize caller-supplied timeouts with `contextx.HTTPClientTimeout` or `contextx.WithoutHTTPTimeout`
- **Build requests explicitly** - Use `http.NewRequestWithContext`. Build paths with `url.PathEscape` and queries with `url.Values`
- **Set headers deliberately** - `Accept` for response format, `Content-Type` only with a body, auth at the HTTP boundary, `User-Agent` for public APIs
- **Keep tokens out of JSON bodies** - Use auth headers when the API expects headers
- **Close and bound bodies** - Close every response body. Decode through `io.LimitReader` or a bounded byte slice. Bound error snippets too
- **Check status before decoding** - Treat 2xx as success. Map specific statuses or error codes to typed errors only when callers branch on them
- **Preserve context cancellation** - Check the request context before wrapping or classifying transport failures
- **Separate transport from non-2xx** - Classify transport errors only when callers branch on that classification
- **Wrap with service, method, and path** - Do not log and return the same error

## Client Operations

Structure as: validate/build → execute → classify → decode.

```go
func (c Client) Team(ctx context.Context, name string, token string) (Team, error) {
	var team Team
	path := "/v1/teams/" + url.PathEscape(name)
	if err := c.do(ctx, http.MethodGet, path, token, nil, &team); err != nil {
		return Team{}, fmt.Errorf("load team %s: %w", name, err)
	}
	return team, nil
}
```

A small `do` helper keeps repeated HTTP behavior consistent:

```go
func (c Client) do(ctx context.Context, method string, path string, token string, body any, out any) error {
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL()+path, encodeBody(body))
	if err != nil {
		return fmt.Errorf("build %s %s request: %w", method, path, err)
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}

	response, err := c.httpClient().Do(request)
	if err != nil {
		if ctxErr := request.Context().Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return decodeErrorResponse(method, path, response)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxBodyBytes)).Decode(out); err != nil {
		return fmt.Errorf("decode %s %s response: %w", method, path, err)
	}
	return nil
}
```

## Downloads

Validate server-provided URLs before requesting them and before following
redirects. Enforce allowed schemes and hosts for model and gateway artifacts.

Use short explicit deadlines for metadata, probes, and release checks. Do not
put short deadlines on long user-requested downloads; stream and cancel through
the caller context.

For artifacts, validate what was downloaded: content length when available,
range headers for partial downloads, checksums, signatures, and final file size.
Use `Accept-Encoding: identity` when byte ranges or content length matter.

## Tests

Use `httptest.Server` for end-to-end client behavior and `RoundTripper` fakes
for narrow transport behavior. Assert method, path, query, headers, auth, and
body shape. Cover non-2xx, malformed JSON, oversized bodies, transport failures,
timeouts, and cancellation.

```go
func TestClientTeam(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/teams/acme" {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer token" {
			t.Fatalf("auth header = %q", got)
		}
		_ = json.NewEncoder(w).Encode(Team{Name: "acme"})
	}))
	defer server.Close()

	got, err := NewClient(server.URL, server.Client()).Team(context.Background(), "acme", "token")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "acme" {
		t.Fatalf("team = %#v", got)
	}
}
```
