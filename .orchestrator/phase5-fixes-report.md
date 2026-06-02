# Phase 5 Fixes Report

**Branch:** `issue-4-token-auth`
**Date:** 2026-06-01
**Status:** All 8 fixes applied; `go vet` clean; all tests pass (race-free).

---

## Fix 1 — Token rotation must affect the live server (MUST FIX)

**Applied.**

`TokenStore` gained a `current string` field protected by `sync.RWMutex` and a `Current() string` read method. `LoadOrGenerate` and `Rotate` both write to `s.current` under the write lock. This means `Rotate()` now atomically updates the in-memory token without a restart.

`NewAuthMiddleware` signature changed from `(token string, next http.Handler)` to `(ts *TokenStore, next http.Handler)`. On each request it calls `ts.Current()` to get the live token — no stale closure.

`CompanionHandler` field changed from `token string` to `ts *TokenStore`. `serveManifest` calls `h.ts.Current()` (with a nil guard) so the PWA `start_url` reflects the current token.

`NewPublicServer` signature changed from `(token string, companion *CompanionHandler)` to `(ts *TokenStore, companion *CompanionHandler)`.

`main.go` updated: `NewCompanionHandler(store, hub, ts)` and `NewPublicServer(ts, companion)` now receive the live store.

All test files updated: `auth_test.go` updated all 6 `NewAuthMiddleware` calls to use `tokenStoreWithValue(t, token)`. `public_server_test.go` rewritten with a new `tokenStoreWithValue` helper that seeds a temp file and calls `LoadOrGenerate`. `companion_test.go` updated: nil-token tests pass `nil`, token-specific tests use `tokenStoreWithValue`.

---

## Fix 2 — `t.Context()` requires Go 1.24 (MUST FIX)

**Applied.**

`internal/relay/public_server_test.go` line 70: replaced `t.Context()` with `context.Background()`. Added `"context"` to the import block. `go vet ./...` now reports no errors.

---

## Fix 3 — Token URL logged to world-readable file (MUST FIX — security HIGH)

**Applied (both parts).**

- Removed `logging.Info("companion URL: %s/%s/", baseURL, token)` from `cmd/relay/main.go`. The QR code printed to stdout is sufficient.
- Changed `internal/logging/logger.go` log directory creation from `0755` to `0o700`, and log file creation permissions from `0644` to `0o600` in both the initial open and the log rotation path.

---

## Fix 4 — SSE broadcast uses string concatenation for JSON (MUST FIX)

**Applied.**

`internal/relay/handler.go` line 82 replaced:
```go
// before
h.hub.Broadcast("new-clip", `{"id":"`+id+`"}`)
// after
payload, _ := json.Marshal(map[string]string{"id": id})
h.hub.Broadcast("new-clip", string(payload))
```
`encoding/json` was already imported.

---

## Fix 5 — `r.URL.RawPath` not cleared after prefix strip (SHOULD FIX)

**Applied.**

After `r2.URL.Path = stripped`, added `r2.URL.RawPath = ""` in `auth.go`. An empty `RawPath` tells Go's `net/url` package to use `Path` as-is (per the documented convention). This prevents percent-encoded paths from carrying the un-stripped form to downstream handlers.

---

## Fix 6 — `os.UserHomeDir()` error silently swallowed (SHOULD FIX)

**Applied.**

`cmd/relay/main.go`:
```go
// before
home, _ := os.UserHomeDir()
// after
home, err := os.UserHomeDir()
if err != nil {
    logging.Fatal("cannot determine home directory: %v", err)
}
```

---

## Fix 7 — `WriteTimeout: 30s` kills SSE connections (SHOULD FIX)

**Applied.**

`internal/relay/server.go` `PublicServer.Serve`: `WriteTimeout` changed from `30 * time.Second` to `0` (disabled). Added a comment explaining the rationale (SSE is a long-lived stream; a finite write timeout silently kills companion connections). The ingest server (`NewServer`) retains its 30s write timeout since it only serves short-lived responses.

---

## Fix 8 — `POST /rotate-token` prints no new QR (SHOULD FIX — AC)

**Applied (injection approach).**

`Handler` gained a `qrPrinter func(newToken string) error` field and a `WithQRPrinter` setter method. `handleRotateToken` calls `h.qrPrinter(token)` if non-nil, logging any error. In `main.go`:
```go
ingestSrv.Handler().WithQRPrinter(func(newToken string) error {
    return relay.PrintQR(os.Stdout, baseURL, newToken)
})
```
`Server` now stores its `*Handler` and exposes it via `Handler() *Handler`. Tests that construct handlers directly (via `NewHandler`) are unaffected — nil `qrPrinter` is silently skipped.

---

## Verification

```
go vet ./...           → clean (no output)
safe-test go test ./... → all packages pass
  ok  github.com/ybouhjira/claude-code-tts/internal/relay  0.132s
  ok  github.com/ybouhjira/claude-code-tts/internal/server 0.557s
  ok  github.com/ybouhjira/claude-code-tts/internal/audio  (cached)
  ok  github.com/ybouhjira/claude-code-tts/internal/tts    (cached)
safe-test go test -race ./internal/relay/... → pass (1.149s)
go build ./cmd/relay/... → clean
```

**Test count: 50+ tests across `relay`, `server`, `audio`, `tts` packages — all passing.**

---

## Files Changed

- `internal/relay/auth.go` — TokenStore current field + RWMutex + Current(); NewAuthMiddleware takes *TokenStore; RawPath cleared
- `internal/relay/companion.go` — CompanionHandler.ts field *TokenStore; serveManifest uses ts.Current()
- `internal/relay/handler.go` — qrPrinter field + WithQRPrinter; JSON marshal instead of concat
- `internal/relay/server.go` — NewPublicServer takes *TokenStore; WriteTimeout=0; Server stores handler
- `cmd/relay/main.go` — UserHomeDir error handled; token log line removed; updated callers; QR printer wired
- `internal/logging/logger.go` — log dir 0o700, log file 0o600
- `internal/relay/auth_test.go` — all NewAuthMiddleware calls updated to *TokenStore
- `internal/relay/public_server_test.go` — tokenStoreWithValue helper; t.Context() → context.Background()
- `internal/relay/companion_test.go` — nil/tokenStoreWithValue for CompanionHandler
