# Voice Output Mode + Telegram Delivery — Design Spec

- **Date:** 2026-06-08
- **Status:** Approved (design); pending implementation plan
- **Branch:** `feat/voice-mode-telegram`
- **Sub-project:** 2 of 4 (builds on the provider/voice foundation already merged to `main`).

## 1. Problem & Motivation

The user wants two things:
1. **An easy on/off switch** for Claude's voice that also lets them pick *where* it goes — without editing files, setting env vars, or restarting.
2. **Telegram delivery** — get Claude's spoken audio on their phone as a Telegram message, using a bot they already have. This is a much simpler alternative to the existing relay + PWA + Web-Push + Tailscale-Funnel "phone companion."

Today the `speak` MCP tool always synthesizes and plays locally; there is no destination control and no Telegram path.

## 2. Goals

1. A persistent **voice mode** with four values: `off | computer | phone | both`. Default `computer` (current behavior — backward compatible).
2. The mode is changeable **two ways**, both reading/writing the same saved state:
   - **MCP tool** (`tts_output`) so Claude flips it when the user says "turn the voice off" / "send to my phone" / "use both".
   - **CLI command** (`speak-text mode <value>`).
3. **Telegram delivery**: send synthesized audio to the user's Telegram chat via the Bot API (`sendAudio`), reusing the existing OpenAI/Grok synthesis. Delivery is a direct HTTPS POST from this machine to `api.telegram.org` — it reaches the phone anywhere the machine has internet, with **no relay server, no Tailscale, no PWA**.
4. The MCP `speak` path routes each job to local playback and/or Telegram based on the mode. `off` short-circuits **before** synthesis (no API cost).

## 3. Non-Goals (explicitly deferred / out of scope)

- **Voice-message "bubble"** (Telegram `sendVoice`, which needs OGG/Opus): v1 uses `sendAudio` (MP3), which appears as a tappable audio clip and needs no conversion software. OGG/Opus via ffmpeg is a possible future polish.
- **Telegram with Piper (WAV):** the Telegram path requires an MP3-emitting provider (OpenAI/Grok), mirroring the relay's existing MP3 constraint. Piper stays computer-only.
- **The relay / PWA / auto-speak-on-every-response paths** are untouched. This feature governs the MCP `speak` tool path (the main way Claude speaks on request).
- **Telegram voice *calls*** — impossible via the Bot API.
- **Inbound control from Telegram** (driving Claude from the phone) — future.

## 4. Approved Decisions

- **(a)** Default mode is **`computer`** (no behavior change until the user flips it).
- **(b)** Mode is changeable from chat (MCP tool) **and** the CLI; both persist to a shared state file.
- **(c)** Telegram delivery is a **direct Bot-API call from the worker** — it does NOT use or modify the relay subsystem.
- **(d)** v1 uses `sendAudio` (MP3); Telegram requires an MP3 provider (OpenAI/Grok), not Piper.
- **(e)** Bot **token** comes from an environment variable (like the provider API keys); the **chat id** lives in `config.json` (it is not secret).

## 5. Architecture

### 5.1 Voice-mode store (`internal/voicemode`)

```go
package voicemode

type Mode string // "off" | "computer" | "phone" | "both"

const (
    Off      Mode = "off"
    Computer Mode = "computer"
    Phone    Mode = "phone"
    Both     Mode = "both"
)

type Store struct { /* path + mutex */ }

func NewStore(path string) *Store        // path defaults to ~/.claude/plugins/claude-code-tts/state.json
func DefaultStore() *Store               // uses the default path (CLAUDE_TTS_STATE override)
func (s *Store) Get() Mode               // reads file; missing/invalid -> Computer (default)
func (s *Store) Set(m Mode) error        // validates, writes {"mode": "..."} atomically
func Valid(m Mode) bool
func (m Mode) PlaysLocal() bool          // computer || both
func (m Mode) SendsTelegram() bool       // phone || both
```

- State file: `~/.claude/plugins/claude-code-tts/state.json` = `{"mode":"computer"}`, overridable via `CLAUDE_TTS_STATE`. A separate file from `config.json` so toggling never rewrites the user's hand-edited config. Thread-safe; tolerant of a missing file (returns default).

### 5.2 Telegram sender (`internal/telegram`)

```go
package telegram

type Sender struct { token, chatID, baseURL string; httpClient *http.Client }

func NewSender(token, chatID string) *Sender // baseURL defaults to https://api.telegram.org
func (s *Sender) SendAudio(ctx context.Context, audio []byte, caption string) error
```

- `SendAudio` POSTs `multipart/form-data` to `<baseURL>/bot<token>/sendAudio` with fields `chat_id`, `caption` (optional, e.g. the spoken text), and the file part `audio` (`clip.mp3`, `audio/mpeg`).
- Non-2xx → error including Telegram's `description`. Injectable `baseURL` so tests run against `httptest` (no real network/token).
- Token and chatID are validated as non-empty by the caller before use.

### 5.3 Config (`internal/ttsconfig`)

`Config` gains an optional section:

```json
"telegram": { "bot_token_env": "TELEGRAM_BOT_TOKEN", "chat_id": "123456789" }
```

- `ttsconfig` struct: `Telegram *TelegramConfig` with `BotTokenEnv string` (default `TELEGRAM_BOT_TOKEN`) and `ChatID string`.
- The registry exposes a helper that builds a `*telegram.Sender` from config+env, or returns `(nil, reason)` when the token env is empty or `chat_id` is missing — so the worker can give a clear "Telegram not configured" error.

### 5.4 Worker delivery routing (`internal/server/worker.go`)

`processJob` becomes mode-aware:

1. `mode := modeStore.Get()`.
2. If `mode == off` → mark the job `completed` (status note "muted") and return **without** synthesizing.
3. Resolve provider + synthesize (existing).
4. If `mode.SendsTelegram()`:
   - require `audio.Format == "mp3"`; if not (Piper), fail with a clear message.
   - require a configured `telegram.Sender`; if not, fail with "Telegram not configured".
   - call `sender.SendAudio(ctx, data, caption)`.
5. If `mode.PlaysLocal()` → `player.Play(data, format)` (existing).
6. **Both:** a Telegram failure when `mode == both` is logged but does NOT fail the job if local playback succeeds (best-effort phone delivery); when `mode == phone`, a Telegram failure fails the job.

The worker gains injected dependencies: a `modeStore` (interface `Get() voicemode.Mode`) and an optional `telegramSender` (interface `SendAudio(ctx, []byte, string) error`, nil-safe). Both are wired in `server.New()` from the registry/config; tests inject mocks.

### 5.5 MCP tools (`internal/server/server.go`)

- **New `tts_output`** tool: arg `mode` (`off|computer|phone|both`). Validates, calls `modeStore.Set`, returns confirmation. This is what Claude calls when the user says "turn the voice off", "send voice to my phone", etc. Tool description tells Claude to use it for exactly those phrasings.
- **`tts_status`** result gains a `voice_mode` field showing the current mode (and whether Telegram is configured).

### 5.6 CLI (`cmd/speak-text/main.go`)

- `speak-text mode <off|computer|phone|both>` → set the persistent mode, print confirmation, exit.
- `speak-text mode` (no value) or `speak-text status` → print current mode + whether Telegram is configured.
- `speak-text [-to computer|phone|both] [flags] TEXT` → speak TEXT **now** for testing. `-to` overrides the destination for this one call (defaults to local playback so `speak-text "hi"` stays a local speaker test); `-to phone` is the quick Telegram test. The global mode is unchanged by a direct speak.

## 6. Data Flow

```
        set mode
  ┌──────────────────────────┐
  │  tts_output (MCP) ───┐    │
  │  speak-text mode ────┼──► voicemode.Store (state.json)
  └──────────────────────┘    ▲
                              │ Get()
  Claude speak(text) ─► worker.processJob
                              │  mode == off  ─► done (no synth, no cost)
                              │  else synth (OpenAI/Grok ─► MP3)
                   ┌──────────┴───────────┐
            PlaysLocal?               SendsTelegram?
                   │                       │
          audio.Play(mp3)        telegram.SendAudio ─► api.telegram.org ─► phone
```

## 7. Error Handling

- Invalid mode (tool/CLI) → error listing the four valid values; state unchanged.
- `phone`/`both` with Telegram not configured → job fails: "Telegram not configured — set $TELEGRAM_BOT_TOKEN and telegram.chat_id in config.json".
- `phone`/`both` with a non-MP3 provider (Piper) → job fails: "Telegram requires an MP3 provider (OpenAI or Grok)".
- Telegram API non-2xx/network error → wrapped error with Telegram's description; logged. Fails the job only when `mode == phone` (for `both`, local playback still counts as success).
- `off` is never an error — it simply produces no audio and no cost.
- All Telegram calls are `context`-aware (use the request/synthesis context).

## 8. Backward Compatibility

- No state file + no telegram config → mode is `computer`, Telegram absent → behaves exactly like today (local playback via the MCP `speak` tool).
- The provider/registry/playback/relay code is unchanged except the worker's delivery step and `server.New()` wiring.

## 9. Testing Strategy

- **voicemode.Store:** default-on-missing, set→get round-trip, invalid-value rejection, `PlaysLocal`/`SendsTelegram` truth table.
- **telegram.Sender:** against an `httptest` server — assert method/path (`/bot<token>/sendAudio`), multipart fields (`chat_id`, file part), and error on non-2xx (no real network).
- **ttsconfig:** telegram section parsing; registry builder returns a sender when configured and a clear reason when not.
- **worker:** mock `modeStore` + mock player + mock telegram sender; assert routing for each mode (off → neither + no synth; computer → player only; phone → telegram only; both → both); `both` with telegram error still succeeds locally; `phone` with telegram error fails; non-MP3 + phone fails; not-configured + phone fails.
- **server:** `tts_output` sets mode (valid/invalid); `tts_status` includes `voice_mode`.
- **CLI:** `mode`/`status` subcommands; `-to phone` path (unit-level via injected sender where feasible).

## 10. File Change Map

**New**
- `internal/voicemode/voicemode.go` (+ `_test.go`) — `Mode`, `Store`, helpers.
- `internal/telegram/telegram.go` (+ `_test.go`) — `Sender`, `SendAudio`.

**Modified**
- `internal/ttsconfig/config.go` — `TelegramConfig` + parsing.
- `internal/ttsconfig/registry.go` — build a `*telegram.Sender` from config/env (or nil + reason).
- `internal/server/worker.go` — inject `modeStore` + `telegramSender`; mode-aware `processJob` routing.
- `internal/server/server.go` — wire mode store + telegram sender in `New()`; add `tts_output` tool; add `voice_mode` to `tts_status`.
- `cmd/speak-text/main.go` — `mode`/`status` subcommands + `-to` flag.
- `config.example.json` — add the `telegram` section with example values (`bot_token_env` + a placeholder `chat_id`).
- `README.md` / `CLAUDE.md` — document the voice mode, the `tts_output` tool, the CLI commands, and Telegram setup (bot token env + chat_id).

## 11. One-time Telegram setup (for the user, documented in README)

1. The user already has a bot → they provide the **bot token** (from @BotFather).
2. Get the **chat id**: message the bot once, then read it from `https://api.telegram.org/bot<token>/getUpdates` (a small helper/instructions, or `speak-text` could print it). Put it in `config.json` under `telegram.chat_id`.
3. Set `TELEGRAM_BOT_TOKEN` in the environment. Set mode to `phone` or `both`.

## 12. Open Questions

None blocking. The exact `getUpdates` chat-id helper (a `speak-text telegram-setup` convenience vs. plain README instructions) will be settled in the implementation plan.
