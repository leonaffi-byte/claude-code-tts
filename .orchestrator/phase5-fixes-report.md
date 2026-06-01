# Phase 5 Review Fixes Report

**Status:** green  
**Tests:** all pass, race clean  
**Branch:** issue-3-sse-companion

---

## Fixes Applied

### Fix 1 — Replace `%q` with `%s` for UUID JSON in handler.go

File: `internal/relay/handler.go`

Changed `fmt.Sprintf(`{"id":%q}`, id)` to `fmt.Sprintf(`{"id":"%s"}`, id)`.

`%q` is Go's string quoting verb — it adds double quotes AND backslash-escapes special characters. For a JSON template literal it is semantically wrong even though it accidentally works for UUIDs (which contain only hex and dashes). Using `%s` inside a JSON string literal is explicit and correct.

### Fix 2 — Deduplicate handleClip into a shared package-level function

Files: `internal/relay/handler.go`, `internal/relay/companion.go`

Both `Handler.handleClip` and `CompanionHandler.handleClip` had identical ~10-line bodies (store.Get + 404 + write audio bytes). Extracted to a package-level function:

```go
// serveClip writes the stored clip identified by id to w, or 404 if not found.
func serveClip(w http.ResponseWriter, store *ClipStore, id string)
```

Both callers now delegate to `serveClip` after their method check and ID extraction. Method validation (GET-only) stays in each caller, as routing is their responsibility.

### Fix 3 — Add SSE subscriber cap to SSEHub

Files: `internal/relay/sse.go`, `internal/relay/companion.go`

Added `maxSubs int` field to `SSEHub` struct. `NewSSEHub()` defaults it to 100. `Subscribe()` checks the cap under the write lock (before creating the channel or generating a UUID) and returns `"", nil, func() {}` when at capacity.

**Important ordering fix:** The `handleEvents` method previously called `w.WriteHeader(http.StatusOK)` before calling `hub.Subscribe()`, which made it impossible to return a 503 after the fact. The subscribe call was moved before any header writes so a 503 can be correctly returned when the hub is full.

In `handleEvents` (companion.go), the nil channel return is now checked:
```go
_, ch, cancel := h.hub.Subscribe()
if ch == nil {
    http.Error(w, "too many connections", http.StatusServiceUnavailable)
    return
}
```

### Fix 4 — Add security headers to companion page response

File: `internal/relay/companion.go`

In `handleRoot`, three security headers are now set before `WriteHeader`:

```go
w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'")
w.Header().Set("X-Content-Type-Options", "nosniff")
w.Header().Set("X-Frame-Options", "DENY")
```

`unsafe-inline` is required because the companion page uses inline `<script>` tags. A nonce-based CSP would require server-side HTML templating and is out of scope.

### Fix 5 — Validate clip ID format in companion.html before fetch

File: `internal/relay/web/companion.html`

In the `new-clip` SSE event handler, after parsing the JSON data and extracting `id`, a UUID format check is applied before using the value in a fetch URL:

```javascript
var uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
if (!uuidPattern.test(id)) {
  console.warn('TTS: received malformed clip ID, ignoring');
  return;
}
```

This prevents path traversal or injection through a malformed SSE event.

---

## Tests Added

- `TestSSEHub_MaxSubs_RejectsWhenFull` — verifies subscriber cap enforced, nil channel returned
- `TestSSEHub_MaxSubs_Zero_NoLimit` — verifies maxSubs=0 means unlimited
- `TestCompanionHandler_GetEvents_MaxSubscribers_Returns503` — verifies 503 when hub full
- `TestCompanionHandler_GetRoot_SecurityHeaders` — verifies CSP, X-Content-Type-Options, X-Frame-Options

---

## Files Changed

1. `internal/relay/handler.go` — Fix 1 (%q→%s), Fix 2 (extract serveClip)
2. `internal/relay/companion.go` — Fix 2 (use serveClip), Fix 3 (nil ch check + reorder subscribe before WriteHeader), Fix 4 (security headers)
3. `internal/relay/sse.go` — Fix 3 (maxSubs field + cap check in Subscribe)
4. `internal/relay/web/companion.html` — Fix 5 (UUID validation)
5. `internal/relay/sse_test.go` — new cap tests
6. `internal/relay/companion_test.go` — new security header + 503 tests

---

## Concerns / Risks

None. All changes are backward compatible. The subscriber cap defaults to 100 which is generous for a local dev tool. The ordering fix (subscribe before WriteHeader) is semantically correct — SSE headers should only be sent once we know the connection can be accepted.
