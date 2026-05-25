# s46 API contract

This is the server contract exercised by `s46-cli/internal/api`.

## Conventions

- Base URL defaults to `https://api.s46.dev`; development shells can override it with `S46_API_BASE_URL`.
- Production harness traffic uses `https://gateway.s46.dev`; wildcard tenant hosts are not part of the contract.
- Teams are canonical `@org/team` slugs, for example `@s46/engineering`.
- The scheduling/data-residency field is `region`.
- Authenticated endpoints require `Authorization: Bearer <accessToken>`.
- Access tokens are never serialized into JSON request bodies.
- Error responses use `{"error":{"code":"forbidden","message":"human-readable detail"}}`.

Known error codes mapped by the CLI: `authorization_pending`, `expired`, `not_invited`, `authenticate_first`, `unauthorized`, `forbidden`.

## Auth and account

### `POST /v1/auth/device/start`

```json
{"email":"dev@example.com","deviceId":"dev-laptop","deviceName":"Dev laptop"}
```

Response `202 Accepted`:

```json
{
  "deviceCode": "opaque-device-code",
  "userCode": "ABCD-EFGH",
  "verificationUri": "https://api.s46.dev/v1/auth/magic/consume?token=...",
  "intervalSeconds": 1,
  "expiresAt": "2026-05-23T00:00:00Z"
}
```

### `POST /v1/auth/device/poll`

```json
{"deviceCode":"opaque-device-code"}
```

Pending approval returns `428 authorization_pending`. Approved login returns a token set.

### `POST /v1/auth/token/refresh`

```json
{"refreshToken":"opaque-refresh-token","account":"dev@example.com"}
```

Response:

```json
{
  "account": "dev@example.com",
  "team": "@s46/engineering",
  "deviceId": "dev-laptop",
  "accessToken": "opaque-access-token",
  "refreshToken": "opaque-refresh-token",
  "expiresAt": "2026-05-23T01:00:00Z"
}
```

### `GET /v1/me`

```json
{"email":"dev@example.com","organization":"s46","team":"@s46/engineering","role":"owner"}
```

### Devices

- `GET /v1/devices`
- `DELETE /v1/devices/{deviceId}`

## Teams

### `GET /v1/teams/{name}`

Path-escape the team id: `@s46/engineering` becomes `%40s46%2Fengineering`.

Optional query parameters: `endpoint`, `region`, `defaultModel`.

Response:

```json
{
  "name": "@s46/engineering",
  "organizationSlug": "s46",
  "slug": "engineering",
  "endpoint": "https://gateway.s46.dev",
  "region": "EU-OPO",
  "workerHosts": [],
  "defaultModel": "s46/kimi-k2.6",
  "models": ["s46/kimi-k2.6"]
}
```

## Sessions

All session list/action endpoints are authenticated and scoped with `?team=@org/team`.

- `GET /v1/sessions?team={team}`
- `POST /v1/sessions/{sessionId}/detach?team={team}`
- `POST /v1/sessions/{sessionId}/resume?team={team}`
- `POST /v1/sessions/{sessionId}/attach?team={team}`
- `POST /v1/sessions/{sessionId}/land?team={team}`

Detach request:

```json
{"sessionId":"@dscape/auth-redirect-fix","harness":"claude-code","team":{"name":"@s46/engineering"}}
```

Attach response:

```json
{"sessionId":"@dscape/auth-redirect-fix","url":"https://api.s46.dev/v1/sessions/dscape%2Fauth-redirect-fix/stream?team=%40s46%2Fengineering","protocol":"sse"}
```
