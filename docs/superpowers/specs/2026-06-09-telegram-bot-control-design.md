# Interactive Telegram Bot Control + Cost Info — Design Spec

- **Date:** 2026-06-09
- **Status:** Approved (design); pending implementation plan
- **Branch:** `feat/telegram-bot-control`
- **Builds on:** the voice-mode + Telegram delivery work already on `main` (`internal/telegram`, `internal/voicemode`, mode-aware worker, voice messages via Opus).

## 1. Problem & Motivation

Telegram delivery is currently one-way (Claude → phone). The user wants the bot to become **interactive**, controlled from their phone:
1. **Pick a voice** from the bot, with audio **demos** of each voice and tap-to-select buttons.
2. **Pick a model** (e.g. `tts-1`, `tts-1-hd`, `gpt-4o-mini-tts`) from the bot.
3. See the **estimated cost (in cents) + model + voice** on every voice message Claude sends.

The chosen voice/model must drive what Claude actually speaks.

## 2. Goals

1. A background **bot poller** in `tts-server` that long-polls Telegram `getUpdates` while Claude Code is open, handling text commands and inline-button taps. Started when Telegram is configured; stopped on shutdown.
2. **Commands:** `/voices` (demo each voice + a "Use this" button), `/model` (buttons per model), `/menu` (show current voice/model + buttons), `/help` (list commands).
3. **Button taps** (callback queries) set the current voice / model and confirm back.
4. A persisted **voice/model selection** that the speak path applies as an override, so the bot's choice drives Claude's speech.
5. Every Telegram voice message carries a **caption** with the estimated cost in cents, the model, and the voice — e.g. `0.03¢ · tts-1 · onyx`.
6. **Security:** the poller only acts on updates from the configured `chat_id`; all others are ignored.

## 3. Non-Goals (deferred)

- **Always-on listening** (when Claude Code is closed) — the poller lives in `tts-server`, which only runs while Claude Code is open (user's explicit choice). A standalone daemon is future work.
- **Switching provider** (OpenAI ↔ Grok) from the bot — voice/model control is scoped to the *current* provider (OpenAI by default). Provider switching stays a config edit.
- **Webhooks** — long-polling only (no public server needed).
- **Multi-user** — single configured `chat_id`.
- **Exact billing** — cost is an *estimate* from a per-model price table (good enough to gauge spend); `gpt-4o-mini-tts` is token-priced so its number is approximate.

## 4. Approved Decisions

- **(a)** Listener runs inside `tts-server` (while Claude Code is open).
- **(b)** Selection UX is **tap-buttons** (Telegram inline keyboards), with `/voices` playing a demo per voice.
- **(c)** Cost shown in **cents** with 2 decimals (e.g. `0.03¢`).
- **(d)** The bot controls **voice + model** for the current provider; provider switching is out of scope.
- **(e)** Only the configured `chat_id` may control the bot.

## 5. Architecture

### 5.1 Telegram API additions (`internal/telegram`)

Extend the existing `Sender` (which already has `Send`/`SendAudio`/`sendFile` + token redaction):

```go
// Update is the subset of a Telegram update we use.
type Update struct {
    UpdateID      int            `json:"update_id"`
    Message       *Message       `json:"message,omitempty"`
    CallbackQuery *CallbackQuery `json:"callback_query,omitempty"`
}
type Message struct {
    Text string `json:"text"`
    Chat Chat   `json:"chat"`
}
type Chat struct{ ID int64 `json:"id"` }
type CallbackQuery struct {
    ID      string   `json:"id"`
    Data    string   `json:"data"`
    Message *Message `json:"message,omitempty"`
    From    struct{ ID int64 `json:"id"` } `json:"from"`
}

// InlineButton/Keyboard model Telegram inline keyboards.
type InlineButton struct {
    Text         string `json:"text"`
    CallbackData string `json:"callback_data"`
}

func (s *Sender) GetUpdates(ctx context.Context, offset int, timeoutSecs int) ([]Update, error)
func (s *Sender) AnswerCallback(ctx context.Context, callbackID, text string) error
func (s *Sender) SendMessage(ctx context.Context, text string, keyboard [][]InlineButton) error
```

- `Send`/`sendFile` gain an optional inline keyboard + caption (so a demo voice message can carry a "Use this" button). The current `Send(ctx, audio, format, caption)` stays; add `SendWithKeyboard(ctx, audio, format, caption, keyboard)` (or an internal variant) so callers that don't need a keyboard are unchanged.
- `GetUpdates`: GET `<base>/bot<token>/getUpdates?offset=<o>&timeout=<t>` (long poll); parse `result`. Token redacted from errors (existing pattern).
- All chat ids compared as `int64`; the configured `chat_id` (a string in config) is parsed to `int64` once.

### 5.2 Selection state (`internal/voicemode`, new file `settings.go`)

A persisted, thread-safe store for the user's current voice + model:

```go
// state file: ~/.claude/plugins/claude-code-tts/voice-settings.json
// {"voice":"onyx","model":"tts-1-hd"}
type Settings struct { Voice, Model string }
type SettingsStore struct { /* path + mutex */ }
func (s *SettingsStore) Get() Settings
func (s *SettingsStore) SetVoice(v string) error
func (s *SettingsStore) SetModel(m string) error
```

A separate file from `state.json` (voice mode) and `config.json` (provider/profiles). Empty values mean "use the profile default".

### 5.3 Speak path applies the selection

In the worker's `processJob` resolution (after profile/provider resolution, before synth): apply the settings as overrides **only when the job didn't specify them explicitly** (so an explicit `speak` tool `voice`/`model` arg still wins):

- if `settings.Voice != "" && job.Voice == ""` → `req.Voice = settings.Voice`
- if `settings.Model != "" && job.Model == ""` → `req.Model = settings.Model`

Precedence: explicit tool arg > bot selection > profile default. The CLI applies the same overrides.

### 5.4 Cost estimate (`internal/cost`)

```go
// CentsFor returns the estimated cost in cents for synthesizing n characters
// with the given provider+model, from a static price table.
func CentsFor(provider, model string, chars int) float64
```

- Price table (USD per 1M characters, approximate published rates): openai `tts-1`≈15, `tts-1-hd`≈30, `gpt-4o-mini-tts`≈ (token-priced; use an approximate char rate, documented as an estimate); grok ≈ its published rate. Unknown model/provider → 0 (caption omits cost or shows `~`).
- `cents = chars * (usdPerMillion / 1_000_000) * 100`.

### 5.5 Caption on every Telegram voice message

When the worker (or CLI) delivers to Telegram, it builds a caption: `fmt.Sprintf("%.2f¢ · %s · %s", cents, model, voice)` and passes it to `Send`. The worker already knows `req.Voice`, the effective model, `provider.Name()`, and `len(job.Text)`. (When the model is empty, show the provider's default model name, e.g. `tts-1`.)

### 5.6 Bot poller (`internal/botcontrol`)

```go
type Poller struct { /* sender, settings, registry/provider info, chatID, log */ }
func NewPoller(sender *telegram.Sender, settings *SettingsStore, ...) *Poller
func (p *Poller) Run(ctx context.Context) // loop until ctx cancelled
```

- Loop: `updates := sender.GetUpdates(ctx, offset, 30)`; for each update with `chat.id == p.chatID` (else ignore), dispatch:
  - **command** (`message.Text` starts with `/`): `/voices`, `/model`, `/menu`, `/help` (see §5.7). Advance `offset = update.UpdateID + 1`.
  - **callback_query**: parse `data` (`voice:<name>` / `model:<name>`), update settings, `AnswerCallback("✓ set …")`, optionally `SendMessage` confirmation.
- Started from `server.New()` in a goroutine when a Telegram sender exists; cancelled on `Shutdown()`.
- Errors are logged (token redacted) and the loop backs off briefly on transient failures; it never crashes the server.

### 5.7 Command behaviors

- `/voices` — for each voice in the current provider's `Voices()` list: synthesize a short fixed demo phrase ("Hi, this is the <name> voice.") in Opus, `Send` it as a voice message with an inline keyboard `[[ {text:"✅ Use <name>", callback_data:"voice:<name>"} ]]`. (Cost note: this synthesizes one short clip per voice each time.)
- `/model` — `SendMessage("Pick a model:", [[tts-1],[tts-1-hd],[gpt-4o-mini-tts]])` with `callback_data:"model:<name>"`.
- `/menu` — `SendMessage` showing current voice + model + the same buttons.
- `/help` — text listing the commands.
- Unknown command → short help hint.

## 6. Data Flow

```
Telegram  ──getUpdates(long poll)──►  botcontrol.Poller (in tts-server)
   ▲                                      │  /voices → synth demos → Send (voice + buttons)
   │ button tap (callback)                │  /model  → SendMessage (buttons)
   └──────────────────────────────────────┘  tap → SettingsStore.SetVoice/SetModel + AnswerCallback

Claude speak ─► worker.processJob ─► resolve profile
                                     ─► apply SettingsStore overrides (voice/model)
                                     ─► synth (Opus for Telegram) ─► caption = cost·model·voice ─► Send
```

## 7. Error Handling & Security

- **Authorization:** updates whose `chat.id` (message) or `from.id`/`message.chat.id` (callback) ≠ configured `chat_id` are ignored silently. This prevents strangers who find the bot from burning credits or changing settings.
- `GetUpdates` transient errors → log (redacted), short sleep, retry; never exit the loop.
- A demo synthesis failure for one voice → log + skip that voice, continue others.
- Invalid callback data → `AnswerCallback("unrecognized")`, ignore.
- Settings file write failure → `AnswerCallback("couldn't save")`.
- The poller goroutine is cancelled cleanly on server shutdown (ctx).
- Token never appears in any logged error (reuse the existing redaction).

## 8. Backward Compatibility

- No Telegram configured → no poller starts; everything behaves as today.
- No `voice-settings.json` → no overrides; profile defaults apply (today's behavior).
- The caption is additive; existing send behavior is unchanged otherwise.

## 9. Testing Strategy

- **telegram:** `GetUpdates` parses a sample updates JSON (httptest); `AnswerCallback`/`SendMessage` hit the right paths with the keyboard JSON; inline-keyboard serialization; token redaction on errors.
- **cost:** `CentsFor` for known models (exact arithmetic) and unknown (0); a representative char count → expected cents.
- **SettingsStore:** default-empty, set/get voice + model round-trip, persistence.
- **worker/CLI override:** settings applied when job doesn't specify; explicit job voice/model wins; caption built correctly (cost·model·voice).
- **botcontrol.Poller:** with a mock sender, feed updates (a `/voices` command, a `model:` callback, an update from a wrong chat id) and assert: demos requested, settings updated, wrong-chat ignored, offset advances. Use an injected clock/cancelable ctx so the loop test terminates.

## 10. File Change Map

**New**
- `internal/cost/cost.go` (+ test) — price table + `CentsFor`.
- `internal/botcontrol/poller.go` (+ test) — the command/callback loop.
- `internal/voicemode/settings.go` (+ test) — `Settings`/`SettingsStore`.

**Modified**
- `internal/telegram/telegram.go` (+ test) — `GetUpdates`, `AnswerCallback`, `SendMessage`, inline-keyboard + caption support, `Update`/`Message`/`CallbackQuery`/`InlineButton` types.
- `internal/server/server.go` — start/stop the poller when Telegram is configured.
- `internal/server/worker.go` — apply settings overrides; build the cost·model·voice caption for Telegram.
- `cmd/speak-text/main.go` — apply settings overrides + caption (so CLI `-to phone` matches).
- `README.md` / `CLAUDE.md` — document the bot commands + cost caption.

## 11. Open Questions

None blocking. The exact `gpt-4o-mini-tts` and Grok per-character rates will be set from published pricing in the plan (documented as estimates); the demo phrase wording is a one-line constant.
