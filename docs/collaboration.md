# API ↔ CLI collaboration

## 2026-05-17 — Implement invitation-gated magic-link auth and device management

Status: DONE

The API auth contract changed. Please update `s46-cli` accordingly.

### Login flow

1. `s46 login` must send user-provided device identity with the email:

```http
POST /v1/auth/device/start
Content-Type: application/json
```

```json
{
  "email": "dscape@acme.s46.dev",
  "deviceId": "<user-chosen-stable-id>",
  "deviceName": "<human display name>"
}
```

The API returns `202 Accepted`:

```json
{
  "deviceCode": "s46_device_...",
  "userCode": "WXYZ-1234",
  "verificationUri": "https://s46.dev/v1/auth/magic/consume",
  "intervalSeconds": 2,
  "expiresAt": "..."
}
```

2. Do not assume `/device` serves a browser page. It is now JSON-only and authenticated by bearer token or the browser session cookie set by magic-link consumption. An unauthenticated `/device` call returns `401 authenticate_first`.
3. For local development, tell the user to open the magic-link URL logged by the API server. Production email sending is not implemented yet.
4. Poll with only the device code; remove `userHint` from the poll body:

```json
{"deviceCode":"s46_device_..."}
```

5. Polling still returns `428 authorization_pending` until the magic link is consumed, then returns tokens:

```json
{
  "account": "dscape@acme.s46.dev",
  "deviceId": "<device id>",
  "accessToken": "s46_access_...",
  "refreshToken": "s46_refresh_...",
  "expiresAt": "..."
}
```

Handle `403 not_invited` with a clear invitation-only message.

### Device management commands

Add commands to manage paired devices for the current account:

- list devices: `GET /v1/devices` with bearer auth
- delete/revoke device: `DELETE /v1/devices/{deviceId}` with bearer auth

Device response shape:

```json
{
  "devices": [
    {
      "id": "dev-laptop",
      "name": "Dev laptop",
      "createdAt": "...",
      "lastSeenAt": "..."
    }
  ]
}
```

Self-revocation is allowed. If the CLI deletes the current device, clear local credentials and require login again.

### Compatibility notes

- Team/session routes still require bearer auth in prod.
- Refresh tokens remain rotating and are now device-bound.
- Device revocation immediately invalidates access and refresh tokens for that device.

Summary: implemented magic-link device login request bodies, poll-with-device-code-only, clear not-invited handling, token/device metadata persistence, `s46 devices` listing, current-device revocation with local logout, and local/API E2E validation against the magic-link flow.
