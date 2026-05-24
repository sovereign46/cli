# S46 API contract

This is the server contract currently exercised by the CLI HTTP client in `internal/api`.

## Conventions

- Base URL defaults to `https://api.s46.dev`; development shells can override it with `S46_API_BASE_URL`.
- Requests send `Accept: application/json`.
- Requests with JSON bodies send `Content-Type: application/json`.
- Authenticated endpoints require `Authorization: Bearer <accessToken>`.
- Access tokens are never serialized into JSON request bodies.
- Error responses use:

```json
{"error":{"code":"forbidden","message":"human-readable detail"}}
```

Known error codes mapped by the CLI: `authorization_pending`, `expired`, `not_invited`, `authenticate_first`, `unauthorized`, `forbidden`.

## Auth and account

### `POST /v1/auth/device/start`

Request:

```json
{"email":"dev@example.com","deviceId":"dev-laptop","deviceName":"Dev laptop"}
```

Response:

```json
{
  "deviceCode": "opaque-device-code",
  "userCode": "ABCD-EFGH",
  "verificationUri": "https://s46.dev/v1/auth/magic/consume",
  "intervalSeconds": 1,
  "expiresAt": "2026-05-23T00:00:00Z"
}
```

### `POST /v1/auth/device/poll`

Request:

```json
{"deviceCode":"opaque-device-code"}
```

Response: token set.

### `POST /v1/auth/token/refresh`

Request:

```json
{"refreshToken":"opaque-refresh-token","account":"dev@example.com"}
```

Response:

```json
{
  "account": "dev@example.com",
  "deviceId": "dev-laptop",
  "accessToken": "opaque-access-token",
  "refreshToken": "opaque-refresh-token",
  "expiresAt": "2026-05-23T01:00:00Z"
}
```

### `GET /v1/me`

Authenticated. Response:

```json
{"email":"dev@example.com","team":"acme"}
```

### `GET /v1/devices`

Authenticated. Response:

```json
{"devices":[{"id":"dev-laptop","name":"Dev laptop","createdAt":"2026-05-23T00:00:00Z","lastSeenAt":"2026-05-23T00:00:00Z","lastSeenIp":"203.0.113.9"}]}
```

### `DELETE /v1/devices/{deviceId}`

Authenticated. Returns `204 No Content` on success.

## Teams

### `GET /v1/teams/{name}`

Authenticated. Optional query parameters: `endpoint`, `lane`, `defaultModel`.

Response:

```json
{
  "name": "acme",
  "endpoint": "https://acme.s46.dev",
  "lane": "EU-OPO",
  "boxes": ["box-01.acme.s46.dev"],
  "defaultModel": "s46/kimi-k2.6",
  "models": ["s46/kimi-k2.6"]
}
```

## Sessions

All session list/action endpoints are authenticated and scoped with `?team=<teamName>`.

### `GET /v1/sessions?team={team}`

Response:

```json
{"sessions":[{"id":"@dscape/auth-redirect-fix","state":"queued","harness":"claude-code","location":"scheduler:job_046","lane":"EU-OPO","model":"s46/kimi-k2.6","age":"0m","spent":"€0.00","task":"fix auth redirect"}]}
```

### `POST /v1/sessions/{sessionId}/detach?team={team}`

Request:

```json
{"sessionId":"@dscape/auth-redirect-fix","harness":"claude-code","team":{"name":"acme"}}
```

Response: queued session object with `location:"scheduler:<jobId>"`.

### `POST /v1/sessions/{sessionId}/resume?team={team}`

Remote resume is the default. Use `target:"local"` to request local materialization.

Request:

```json
{"sessionId":"@dscape/auth-redirect-fix","session":{"id":"@dscape/auth-redirect-fix"},"team":{"name":"acme"},"target":"remote"}
```

Response: session object.

### `POST /v1/sessions/{sessionId}/attach?team={team}`

Request:

```json
{"sessionId":"@dscape/auth-redirect-fix","team":{"name":"acme"}}
```

Response:

```json
{"sessionId":"@dscape/auth-redirect-fix","url":"https://api.s46.dev/v1/sessions/dscape%2Fauth-redirect-fix/stream?team=acme","protocol":"sse"}
```

### `POST /v1/sessions/{sessionId}/land?team={team}`

Request:

```json
{"sessionId":"@dscape/auth-redirect-fix","session":{"id":"@dscape/auth-redirect-fix"},"team":{"name":"acme"},"title":"Auth redirect fix"}
```

Response:

```json
{
  "id": "@dscape/auth-redirect-fix",
  "title": "Auth redirect fix",
  "branch": "s46/dscape-auth-redirect-fix",
  "ranOn": ["local checkpoint", "S46 worker VM"],
  "harness": "claude-code",
  "model": "s46/kimi-k2.6",
  "cost": "€0.00",
  "status": "blocked",
  "blockedReason": "github_repository_not_configured",
  "review": {
    "summary": "Ready for policy-gated review.",
    "checklist": ["inspect git diff", "run tests", "run /review", "connect a GitHub App repository"],
    "suggestedCommands": ["git diff", "git status", "s46 session land --json"]
  }
}
```
