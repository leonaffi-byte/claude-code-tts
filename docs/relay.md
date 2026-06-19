# Relay (phone companion) overview

The **relay** (`cmd/relay`) is a small background HTTP service that delivers
Claude's spoken clips to a paired phone without Telegram. It is the most
network-exposed component in this repository, so this document explains what it
does, how it is structured, its token/auth model, and its security posture.

> The relay is launched automatically by the `SessionStart` hook
> (`relay-start.sh`) when `CLAUDE_TTS_ENABLED=true`, and stopped by the
> `SessionEnd` hook (`relay-stop.sh`). If you enable automatic TTS you get a
> network listener; read this page to understand what it is.

## What it does

1. The `auto-speak.sh` Stop hook POSTs the first sentence of each Claude
   response to the relay's **ingest** endpoint.
2. The relay synthesizes the text to audio via the configured cloud provider
   (OpenAI or Grok — Piper is local-only and cannot be used with the relay).
3. The clip is stored in memory and announced over **Server-Sent Events (SSE)**
   to any connected companion PWA, which auto-plays it.
4. If no foreground companion is connected (presence tracking), a **Web Push**
   notification is sent instead so the phone can wake and play the clip.

## Two-port architecture

The relay runs two separate HTTP servers with very different exposure:

| Server | Bind address | Default port | Env override | Exposure |
|--------|--------------|--------------|--------------|----------|
| **Ingest** | `127.0.0.1` (loopback only) | `8765` | `RELAY_PORT` | Local machine only. Never exposed. Accepts Stop-hook `POST /ingest`, serves `/clips/{id}`, and `POST /rotate-token`. |
| **Public** | `0.0.0.0` (all interfaces) | `8766` | `PUBLIC_PORT` | Reachable from the LAN and, behind Tailscale Funnel, the internet. Serves the companion PWA, SSE stream, clip proxy, Web Push subscription, and the VAPID public key. |

Both servers share the same in-memory clip store and SSE hub, so a clip ingested
on the loopback side is immediately visible to the public side.

`RELAY_PORT` and `PUBLIC_PORT` are validated at startup (numeric, 1–65535); an
invalid value fails fast with a clear error before any listener binds.

## Token model

The public server is gated by a single **bearer token** that is embedded in the
URL path as a prefix: every protected request must be of the form
`/<token>/…`. The auth middleware strips the prefix before forwarding to the
companion handler; any request whose first path segment is not the token gets a
`404 Not Found`.

- **Persistence.** The token is generated on first run and persisted to
  `~/.claude/plugins/claude-code-tts/token` (mode `0600`). It survives relay
  restarts so a paired phone keeps working.
- **Comparison.** The token segment is compared to the secret using
  `crypto/subtle.ConstantTimeCompare` after a length check, avoiding a timing
  side-channel.
- **Rotation.** An authenticated `POST /rotate-token` generates a fresh token,
  overwrites the file, and immediately invalidates the old one in memory. A new
  QR code is printed so you can re-pair.
- **Abuse mitigation.** Failed auth attempts are throttled per client IP (a
  lightweight in-memory token bucket) and incur a small constant delay, as
  defence-in-depth against unauthenticated request flooding.

> **Treat the token like a password.** It is the sole authentication for the
> public server. The token is **not** placed in Web Push payload URLs — the push
> payload carries only the clip ID, and the service worker resolves the clip URL
> relative to its own token-scoped registration scope.

## QR pairing

On startup (and after rotation) the relay prints a QR code encoding
`http://<lan-ip>:<PUBLIC_PORT>/<token>/`. Scan it from the phone to open the
companion at the authenticated URL. If no non-loopback LAN IPv4 can be
determined, the relay logs a warning and falls back to `localhost` (which a
phone cannot reach) so the situation is observable.

## VAPID / Web Push

The relay generates a VAPID keypair on first run (persisted alongside the token)
and exposes the public key via `GET /push/vapid-public-key`. The companion
subscribes with `POST /push/subscribe`; subscriptions are capped, de-duplicated
by endpoint, and pruned when a push service returns `410 Gone`. Subscription
endpoints are validated (https-only, and rejected when they resolve to
loopback/private/link-local/metadata addresses) to limit SSRF exposure. The push
fan-out runs asynchronously with a bounded per-request timeout so the ingest
response is never coupled to push-service latency.

## Companion PWA security headers

The companion HTML is served with a strict Content-Security-Policy
(`default-src 'self'; script-src 'self' 'nonce-…'`) using a fresh per-response
nonce, plus `X-Content-Type-Options: nosniff` and `X-Frame-Options: DENY`. The
SSE stream emits periodic heartbeat comment frames so a silently-dropped peer is
detected via a failing write, freeing its subscriber slot.

## Security posture / threat model

- The **ingest server is loopback-only** and never exposed; only locally-running
  processes (the Stop hook) can inject clips.
- The **public server is meant to sit behind Tailscale Funnel**, which provides
  a stable HTTPS hostname and outbound tunneling without opening inbound
  firewall ports. Do **not** raw-expose `0.0.0.0:PUBLIC_PORT` to the internet
  directly; the token is high-entropy but Funnel adds TLS and avoids inbound
  port exposure.
- A recovered token grants full access to the audio stream and lets an attacker
  register push subscriptions, so rotate the token (`POST /rotate-token`) if you
  suspect compromise.

## Related docs

- [Tailscale Funnel Setup](tailscale-funnel-setup.md) — expose the public server
  over a stable HTTPS hostname.
- [Android End-to-End Verification Checklist](android-verification-checklist.md)
  — validate push and auto-play on a real device.
