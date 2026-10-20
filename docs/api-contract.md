# s46 API contract

This is the current source contract exercised by `s46-cli/internal/api`, not a statement of deployed or stable-release readiness. See [candidate testing](testing-candidate.md) for qualification prerequisites.

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

Optional query parameters sent by the CLI: `endpoint`, `region`, `defaultModel`. These override settings in this response only; they do not persist team configuration or grant model access. In particular, requesting `defaultModel` does not add it to `models`.

For cloud teams, `models` is the authoritative list of currently servable catalog entries allowed by team policy. `modelMetadata` is keyed by those exact public model IDs. Do not invent fallback models or infer runtime limits from model names. `defaultModel` is the configured/requested selection, not proof of authorization; it can be absent from `models`. Cloud `connect` refuses a selected model absent from that list before changing configuration; choose an allowed `--model` or ask an administrator to enable it. Gateway requests still require model authorization.

Pi requires metadata for every listed cloud model: positive `contextWindow` and `maxTokens`, with `maxTokens <= contextWindow`. `reasoning` controls reasoning support; `engine` and `version` identify the runtime. Optional metadata also includes `profileSha256`, `minMemoryBytes`, and `recommendedMemoryBytes`.

Illustrative response (not evidence that this model is deployed or authorized for your team):

```json
{
  "name": "@s46/engineering",
  "organizationSlug": "s46",
  "slug": "engineering",
  "endpoint": "https://gateway.s46.dev",
  "region": "EU-OPO",
  "workerHosts": [],
  "defaultModel": "s46/deepseek-v4-flash-0731",
  "models": ["s46/deepseek-v4-flash-0731"],
  "modelMetadata": {
    "s46/deepseek-v4-flash-0731": {
      "engine": "vllm",
      "version": "0.31.0",
      "contextWindow": 262144,
      "maxTokens": 32768,
      "reasoning": true
    }
  }
}
```

The exact Pi thinking-level mapping and separate Devstral airplane behavior are described in the [candidate request contract](testing-candidate.md#pi-request-contract).

## Sessions

Session listing is authenticated and scoped with `?team=@org/team`.

- `GET /v1/sessions?team={team}`
