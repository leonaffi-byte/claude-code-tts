# Telegram → Claude: two-way voice control (design)

Date: 2026-06-20
Status: Approved (design); ready for implementation planning.

## 1. Summary

Today the plugin is **one-way**: Claude's responses are spoken and (optionally) sent
to Telegram as audio. This feature adds the **return path** — you reply in Telegram
(text **or** voice note) and your reply is injected into the *same live `claude`
session* as your next turn. Voice notes are transcribed and translated to English
before injection, mirroring the user's existing `voice_bot` pipeline.

The goal is the "couch experience": Claude finishes, you get the audio on your phone,
you answer in Telegram, and Claude keeps working at your desk — even while the PC is
locked.

## 2. Goals / non-goals

**Goals (v1)**
- Reply to Claude from Telegram and have it land in your existing terminal session.
- Accept both typed replies and voice notes (voice → STT → translate → inject).
- Work when you are away from the keyboard (PC locked / terminal not focused).
- Reuse the plugin's existing `telegram` and `ttsconfig` packages and the established
  outbound audio path (unchanged).

**Non-goals (v1 — explicitly deferred)**
- Multi-session routing (delivering a reply to a *specific* session among several via
  Telegram reply-threading). v1 supports a single active inbound session.
- Notifying Telegram about mid-tool *permission* prompts (y/n dialogs). v1 only
  surfaces Claude's normal responses (existing audio); you steer by replying.
- Telegram webhook mode. v1 uses outbound long-polling (`getUpdates`).
- Any non-Telegram remote surface.

## 3. Why a wrapper (and why not the alternatives)

The target environment is **Windows + Windows Terminal (ConPTY)** with many
concurrent `claude`/`node` processes. The mechanisms for feeding text into an
*already-running* terminal session were evaluated and rejected:

- `WriteConsoleInput` (AttachConsole): does not reliably reach a **ConPTY**-hosted
  app — input is owned by Windows Terminal, not the console buffer.
- `SendInput` (focus-based, e.g. AutoHotkey): requires the PC unlocked **and** the
  correct terminal tab focused — fatal for the away-from-keyboard use case, and
  cannot disambiguate among many sessions.
- `tmux send-keys`: clean, but Unix-only; not available on a bare Windows terminal.

**Chosen approach:** launch `claude` through a thin **pseudo-terminal wrapper** that
owns the session's input pipe. It proxies the terminal transparently (so it feels
exactly like running `claude`), and can therefore inject replies into stdin reliably,
regardless of focus or lock state. The user aliases `claude → claude-tts run`, so
their habit is unchanged.

## 4. Architecture

```
                Telegram (your phone)
                  │  ▲
   voice/text ────┘  └──── audio out (EXISTING path, unchanged)
        │
        ▼
┌─────────────────────────────────────────────────────────┐
│ claude-tts run  (new cmd; alias `claude`)                │
│                                                          │
│  ┌───────────────┐        ┌──────────────────────────┐  │
│  │ PTY bridge    │        │ Inbound poller (goroutine)│  │
│  │ spawn claude  │        │  getUpdates (chat_id only)│  │
│  │ proxy kbd/out │◄───────│  text → inject            │  │
│  │ inject stdin  │ inject │  voice → getFile→STT→      │  │
│  └──────┬────────┘        │          translate→inject │  │
│         │                 └──────────────────────────┘  │
│         ▼                                                │
│   real `claude` TUI (your normal session)                │
└─────────────────────────────────────────────────────────┘
```

### Components

- **`cmd/claude-run`** (binary invoked as `claude-tts run`; the user aliases `claude`
  to it). Responsibilities: spawn `claude` in a PTY, proxy I/O, host the inbound
  poller, and inject received text.
- **`internal/ptybridge`** (new): cross-platform PTY spawn + transparent proxy +
  `Inject(text)`. Uses a maintained cross-platform PTY library that supports Windows
  **ConPTY** and Unix PTYs. Handles window-resize forwarding and raw-mode passthrough.
- **`internal/stt`** (new): OpenAI `audio/transcriptions`
  (`gpt-4o-mini-transcribe`, OGG input) + translate-to-English, with bounded retries.
  Mirrors `voice_bot/bot/transcriber.py`.
- **`internal/telegram`** (extended): add `GetUpdates` (long-poll) and `GetFile` +
  file download. The existing `Sender` (outbound) is unchanged.
- **`internal/ttsconfig`** (extended): new `telegram.inbound` config block.

The outbound audio path (auto-speak Stop hook → relay/worker → `telegram.Send`) is
**not modified**.

## 5. Data flow (the couch loop)

1. Claude finishes a response → audio reaches Telegram (existing).
2. You send a Telegram **voice note** (or text) from your chat.
3. The wrapper's poller receives the update (filtered to your `chat_id`).
4. Voice: download the OGG via `getFile` → transcribe (`gpt-4o-mini-transcribe`) →
   translate to English. Text: used as-is.
5. The wrapper injects the resulting text + Enter into the live `claude` PTY.
6. Claude processes it as your next turn and responds → audio out (existing).
7. Repeat.

## 6. Configuration

Extend the existing `telegram` block in
`~/.claude/plugins/claude-code-tts/config.json`. STT/translate reuse `OPENAI_API_KEY`.

```json
"telegram": {
  "bot_token_env": "TELEGRAM_BOT_TOKEN",
  "chat_id": "YOUR_CHAT_ID",
  "inbound": {
    "enabled": true,
    "transcribe_model": "gpt-4o-mini-transcribe",
    "translate": true,
    "source_language": "auto",
    "target_language": "English",
    "require_reply": false
  }
}
```

- `enabled` — master switch for the inbound feature.
- `transcribe_model` — STT model (default `gpt-4o-mini-transcribe`).
- `translate` / `target_language` — translate transcribed text before injection
  (default **on**, English). Set `translate: false` for raw transcription.
- `require_reply` — when `true`, only inject when the Telegram message is a **reply**
  to one of Claude's clips (guards against accidental injection). Default `false`.

## 7. Security

Injection drives your real Claude session — it can run tools and edit files as you.
The trust boundary is therefore explicit and narrow:

- **`chat_id` allowlist:** inbound messages are accepted **only** from the configured
  `chat_id`; all others are dropped and logged. (Same boundary as outbound today.)
- **Bot token** stays in an environment variable, never in config or logs.
- **No new public exposure:** `getUpdates` is an outbound long-poll; no inbound port
  or webhook is opened.
- **`require_reply`** is offered as an opt-in extra guard (inject only on replies to
  Claude's clips).
- Documentation must state plainly: anyone who can message this bot can drive Claude
  on your machine — keep the token secret and the `chat_id` correct.

## 8. Error handling

- **STT / translate failure:** reply to Telegram ("couldn't transcribe — try again")
  instead of injecting partial/garbage text. Bounded retries with backoff (like
  `voice_bot`).
- **PTY spawn failure:** fall back to `exec`-ing `claude` directly (no inbound), with
  a one-line warning. The wrapper must never prevent you from working.
- **Telegram poll errors:** exponential backoff + retry; never crash the session.
- **Single-poller lock contention:** run proxy-only and log that inbound is owned by
  another session.

## 9. Concurrency / multi-session

- A **single-poller lockfile** under the plugin state dir ensures only one wrapped
  session consumes Telegram `getUpdates` at a time (the machine runs many `claude`
  processes; we must not have several consuming/splitting updates). The lock holder is
  the "active inbound" session; other wrapped sessions proxy normally without inbound.
- The PTY proxy and the poller run as separate goroutines; injection into the PTY is
  serialized so a reply cannot interleave with live keystrokes mid-line.
- Routing a reply to a *specific* session (reply-threading) is **future work**.

## 10. Testing

- **Unit:** STT client (mock HTTP: success, retry, language-rejection fallback),
  translate, `GetUpdates`/`GetFile` JSON parsing, `chat_id` filtering, injection
  encoding (text + Enter, multi-line handling), lockfile acquire/contention.
- **PTY bridge:** the injection-encoding and proxy plumbing are unit-tested with a
  fake PTY; **real ConPTY proxying is integration/manual-tested**. This is called out
  as the main risk (see below).
- Keep tests hermetic (no real network, no real Telegram, no real `claude`).

## 11. Risks

- **Primary risk — ConPTY proxy fidelity on Windows:** rendering of Claude's TUI,
  window-resize handling, and raw-mode passthrough through a Go-hosted ConPTY are the
  hardest parts. Mitigation: use a maintained cross-platform PTY library and the
  direct-exec fallback so a proxy problem degrades to "no inbound" rather than a
  broken session. A spike to validate the PTY library against the real `claude` TUI
  should be the first implementation step.
- STT cost/latency on long voice notes — bounded by Telegram's voice-note limits;
  acceptable.

## 12. v1 scope checklist

- [ ] `internal/ptybridge`: spawn + transparent proxy + inject (with direct-exec
      fallback).
- [ ] `cmd/claude-run` (`claude-tts run`): wires PTY bridge + inbound poller.
- [ ] `internal/telegram`: `GetUpdates`, `GetFile`, file download.
- [ ] `internal/stt`: transcribe + translate (OpenAI), retries.
- [ ] `internal/ttsconfig`: `telegram.inbound` config.
- [ ] `chat_id` security + `require_reply` toggle.
- [ ] Docs: setup (alias `claude`), security note, config reference.
- [ ] Tests per section 10.

## 13. Open questions (resolved defaults)

- Translate-to-English: **on by default** (configurable).
- `require_reply`: **available, default off**.
- Permission-prompt notifications, multi-session routing, webhook mode: **deferred**.
