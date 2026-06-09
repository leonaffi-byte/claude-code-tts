# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

A Text-to-Speech MCP server plugin for Claude Code written in Go. It converts text to speech using a pluggable provider abstraction (OpenAI, Grok/xAI, or local Piper) and plays audio via platform-native players. Provider selection and voice profiles are configured via `~/.claude/plugins/claude-code-tts/config.json` (or `CLAUDE_TTS_CONFIG`) with runtime overrides via `CLAUDE_TTS_*` environment variables.

## Commands

```bash
# Build
make build              # Creates bin/tts-server

# Run locally (requires OPENAI_API_KEY)
make run

# Test
make test               # Run all tests
go test -v ./internal/server/...  # Run specific package tests

# Lint
make lint               # Runs golangci-lint (auto-installs if missing)

# Format
make fmt

# Install to Claude Code plugins
make install            # Installs to ~/.claude/plugins/claude-code-tts/
```

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│  cmd/tts-server/main.go                                     │
│    Entry point - loads ttsconfig.Registry, creates server   │
│    (no hard OPENAI_API_KEY requirement; keys per-provider)  │
│                                                             │
│  internal/server/                                           │
│    server.go: MCP server setup, tool registration           │
│      - speak(text, profile, provider, voice, speed)         │
│      - tts_status() → returns pool stats as JSON            │
│      - wires voicemode.SettingsStore into WorkerPool        │
│      - starts/stops botcontrol.Poller when a Telegram       │
│        sender and a numeric chat_id are configured          │
│                                                             │
│    worker.go: Worker pool (2 workers, 50-slot queue)        │
│      - Concurrent job processing with goroutines            │
│      - Job history tracking (last 100 jobs)                 │
│      - Atomic counters for processed/failed stats           │
│      - Injectable synthResolver (ttsconfig.Registry) +      │
│        audioPlayer (audio.Player)                           │
│                                                             │
│  internal/tts/                                              │
│    provider.go: Provider interface, Request, Audio types    │
│    openai.go: OpenAIProvider — POST /v1/audio/speech        │
│    grok.go:   GrokProvider   — xAI TTS API, returns MP3     │
│    piper.go:  PiperProvider  — local binary, returns WAV    │
│      (Piper is local-playback only; relay requires OpenAI   │
│       or Grok)                                              │
│                                                             │
│  internal/ttsconfig/                                        │
│    config.go:   Config structs, loadConfig, DefaultConfig   │
│    registry.go: Registry — Load/LoadOrDefault, Resolve,     │
│      ResolveVoice, Default (with CLAUDE_TTS_* env overrides)│
│      TelegramSender() — builds telegram.Sender from config  │
│                                                             │
│  internal/voicemode/                                        │
│    voicemode.go: Mode type (Off/Computer/Phone/Both),       │
│      Valid, PlaysLocal, SendsTelegram; Store.Get/Set        │
│      persisted to ~/.claude/plugins/claude-code-tts/        │
│      state.json (overridable via CLAUDE_TTS_STATE)          │
│    settings.go: Settings{Voice,Model} + SettingsStore       │
│      (Get/SetVoice/SetModel) persisted to               │
│      voice-settings.json (overridable via                   │
│      CLAUDE_TTS_SETTINGS)                                   │
│                                                             │
│  internal/cost/                                             │
│    cost.go: price table for OpenAI TTS models;              │
│      CentsFor(provider, model, chars) → estimated cost;     │
│      EffectiveModel(provider, model) → canonical name;      │
│      ModelsFor(provider) → available model names            │
│                                                             │
│  internal/botcontrol/                                       │
│    poller.go: Poller — long-polls Telegram getUpdates in    │
│      a goroutine started by tts-server; handles /voices     │
│      (audio demos + tap-to-select), /model (button menu),  │
│      /menu, /help (/start); button taps update              │
│      SettingsStore; only the configured chat_id is honored  │
│                                                             │
│  internal/telegram/                                         │
│    telegram.go: Sender — SendAudio, GetUpdates,             │
│      SendMessage, AnswerCallback, SendVoiceWithButton;      │
│      Update/Message/Chat/CallbackQuery/InlineButton types;  │
│      token redacted from all error messages                 │
│                                                             │
│  internal/audio/                                            │
│    player.go: Cross-platform, format-aware audio playback   │
│      - Play(data []byte, format string)                     │
│      - macOS: afplay, Linux: mpv/ffplay/mpg123              │
│      - Windows: MP3 via WPF MediaPlayer, WAV via SoundPlayer│
└─────────────────────────────────────────────────────────────┘
```

## Key Design Decisions

- **Worker Pool Pattern**: Jobs are non-blocking; `speak()` returns immediately after queuing
- **Mutex-Protected Playback**: `audio.Player` ensures no overlapping audio
- **Job Queue**: Channel-based with 50 slots; returns error when full
- **MCP Protocol**: Uses `mcp-go` library for stdio-based communication with Claude Code
- **Provider Abstraction**: `tts.Provider` interface with `Synthesize(ctx, Request) (Audio, error)` implemented by OpenAI, Grok, and Piper
- **Registry Pattern**: `ttsconfig.Registry` loads config + resolves named profiles and env overrides to a `(Provider, Request)` pair
- **Format-Aware Playback**: `audio.Player.Play(data, format)` chooses the right player command for MP3 vs WAV

## Environment

- **Optional** (no hard requirement): `OPENAI_API_KEY` — required only when the OpenAI provider is used
- **Optional**: `XAI_API_KEY` — required only when the Grok provider is used
- **Go Version**: 1.23 (go.mod)

### Configuration env vars

| Variable | Description |
|----------|-------------|
| `CLAUDE_TTS_CONFIG` | Path to config JSON (default: `~/.claude/plugins/claude-code-tts/config.json`) |
| `CLAUDE_TTS_PROFILE` | Named profile to use instead of the configured default |
| `CLAUDE_TTS_PROVIDER` | Explicit provider (`openai`, `grok`, `piper`); bypasses profile selection |
| `CLAUDE_TTS_VOICE` | Override voice for the resolved provider/profile |
| `CLAUDE_TTS_SPEED` | Override speech speed (float) |
| `CLAUDE_TTS_MODEL` | Override model name (e.g. `tts-1-hd`) |
| `CLAUDE_TTS_STATE` | Path to state JSON for persisted voice mode (default: `~/.claude/plugins/claude-code-tts/state.json`) |
| `CLAUDE_TTS_SETTINGS` | Path to JSON for the bot-selected voice/model (default: `~/.claude/plugins/claude-code-tts/voice-settings.json`) |
| `TELEGRAM_BOT_TOKEN` | Telegram bot token (or use `telegram.bot_token_env` in config to name a different env var) |

## MCP Tools

| Tool | Parameters | Description |
|------|------------|-------------|
| `speak` | `text` (required), `profile`, `provider`, `voice`, `speed` | Queue TTS job |
| `tts_status` | none | Get queue/worker stats (includes `voice_mode` and `telegram_configured`) |
| `tts_pause` | none | Pause job processing |
| `tts_resume` | none | Resume job processing |
| `tts_clear` | none | Clear pending jobs |
| `tts_output` | `mode` (required: `off`/`computer`/`phone`/`both`) | Set the voice output destination; persisted across restarts |
