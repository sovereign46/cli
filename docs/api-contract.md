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

A prelaunch response additionally carries `releaseChannel: "prelaunch"`, `productionCertified: false`, `productionAPIPath: false`, a lowercase, non-periodic `sha256:` policy identity, and canonical UTC policy/upstream-credential expiries. It must also carry exactly one each of `X-S46-Release-Channel: prelaunch`, `X-S46-Production-Certified: false`, and `X-S46-Production-API-Path: false`; the CLI rejects header/body disagreement. The CLI persists the body fields in both the team config and original API snapshot and exposes them in connect/status output. It rejects unknown channels, missing or positive production claims on prelaunch, missing/malformed identities, expired or inverted expiries, and catalog/metadata drift. Under this contract any catalog containing `s46/deepseek-v4-flash-0731` requires the complete prelaunch envelope; omission cannot declassify it into ordinary cloud. Stable 0.2.5 does not support this prelaunch contract; qualification requires the exact release-tagged 0.3.0 candidate under test, without claiming that 0.3.0 has been published.

Pi requires metadata for every listed cloud model: positive `contextWindow` and `maxTokens`, with `maxTokens <= contextWindow`. `reasoning` controls reasoning support; `engine` and `version` identify the runtime. For prelaunch these values must be required, validated fields in the protected route policy and copied into the team response; engine and version are 1–64 ASCII characters from `[A-Za-z0-9._+-]`, and the CLI does not infer them from the public or backend model ID. The exact ID `s46/deepseek-v4-flash-0731` must also include a syntactically usable `closureSha256`: exactly 64 bare lowercase hexadecimal characters, with no prefix or whitespace, and not an exact repetition of a shorter hex sequence whose length properly divides 64 (a periodic placeholder). `closureSizeBytes` must be an integer from 1 through 64 MiB (67,108,864 bytes), inclusive. Prelaunch accepts this model only as vLLM 0.31.0 with reasoning enabled, so Pi always emits the explicit thinking-off mapping. Classified/prelaunch catalogs containing this ID require the Pi harness at connect and are rechecked from both active and saved API metadata before launch; ordinary unclassified cloud teams and valid airplane routes retain their existing harness choices. An airplane launch is valid only when the active endpoint is exactly `http://127.0.0.1:8080` and both the default and sole catalog entry are `s46/devstral-small-2-24b`; a durable cloud API snapshot may remain solely for restoration. Pi launch preflight requires the generated `providers.s46` object to contain exactly `baseUrl`, `api`, `apiKey`, `authHeader`, and `models`, rejecting provider headers, compatibility settings, model overrides, or any other extra field. The CLI validates syntax and bounds; it does not sign or independently authenticate the closure or establish production readiness. Unrelated production model IDs are not subject to this closure check. Optional metadata also includes `profileSha256`, `minMemoryBytes`, and `recommendedMemoryBytes`.

Illustrative response (not evidence that this model is deployed or authorized for your team):

```json
{
  "name": "@s46/engineering",
  "organizationSlug": "s46",
  "slug": "engineering",
  "endpoint": "https://gateway.s46.dev",
  "region": "EU-OPO",
  "workerHosts": [],
  "defaultModel": "s46/devstral-small-2-24b",
  "models": ["s46/devstral-small-2-24b"],
  "modelMetadata": {
    "s46/devstral-small-2-24b": {
      "contextWindow": 65536,
      "maxTokens": 4096,
      "reasoning": false
    }
  }
}
```

The exact Pi thinking-level mapping and separate Devstral airplane behavior are described in the [candidate request contract](testing-candidate.md#pi-request-contract).

## Sessions

Session listing is authenticated and scoped with `?team=@org/team`.

- `GET /v1/sessions?team={team}`
