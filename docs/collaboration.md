# API ↔ CLI collaboration

## 2026-05-17 — API needs production-auth capable client calls

Status: DONE

The API can keep dev compatibility for current CLI behavior, but production hardening needs CLI changes:

1. `s46 login` should perform a real device-code flow: start auth, print/open verification URI + user code, then poll until approved/expired.
2. API calls after login should be able to include `Authorization: Bearer <access token>` where required, especially:
   - `GET /v1/teams/{team}`
   - `GET /v1/sessions`
   - `POST /v1/sessions/{id}/detach|resume|attach|land`
3. The CLI should avoid relying on API dev-mode auto-approval. API currently keeps auto-approval only outside `S46_ENV=prod` for compatibility.

Summary: implemented print-before-poll device login with pending/expired handling, added bearer-token propagation for team/session API calls, added token loading/refresh for session operations, and expanded tests for auth headers and production/local URL behavior.

Commit: pending in CLI working tree.

## 2026-05-17 — API make-shell E2E blocked by CLI compile error

Status: DONE

While testing the API via the required normal developer flow:

```sh
cd ~/dev/s46-cli
S46_API_BASE_URL=http://127.0.0.1:18086 make shell
```

`go build ./cmd/s46` fails inside `scripts/shell` with:

```txt
internal/session/service.go:62:56: s.accessToken undefined (type Service has no field or method accessToken)
internal/session/service.go:83:139: s.accessToken undefined (type Service has no field or method accessToken)
internal/session/service.go:103:109: s.accessToken undefined (type Service has no field or method accessToken)
internal/session/service.go:202:139: s.accessToken undefined (type Service has no field or method accessToken)
```

Summary: implemented `session.Service.accessToken`, wired the session service to receive the keyring from CLI commands, and verified `go test ./...` passes.
