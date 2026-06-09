# Claude Code TTS Plugin

[![Go Version](https://img.shields.io/badge/Go-1.23+-00ADD8?style=flat&logo=go)](https://golang.org)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![CI](https://github.com/ybouhjira/claude-code-tts/actions/workflows/ci.yml/badge.svg)](https://github.com/ybouhjira/claude-code-tts/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/ybouhjira/claude-code-tts/branch/main/graph/badge.svg)](https://codecov.io/gh/ybouhjira/claude-code-tts)
[![MCP](https://img.shields.io/badge/MCP-Compatible-green.svg)](https://modelcontextprotocol.io)

A Text-to-Speech MCP server plugin for Claude Code that converts text to speech using a pluggable provider abstraction — OpenAI, Grok (xAI), or local offline Piper — selectable via named voice profiles. Get audio feedback from Claude as you work! See [Providers & Configuration](#providers--configuration) below.

![Demo](demo.gif)

## Features

- **Deterministic Auto-Speak**: Every Claude response is automatically spoken (via Stop hook)
- **6 High-Quality Voices**: alloy, echo, fable, onyx, nova, shimmer
- **Worker Pool Architecture**: Non-blocking queue with concurrent processing
- **Mutex-Protected Playback**: One audio plays at a time, no overlapping
- **Cross-Platform**: macOS (afplay), Linux (mpv/ffplay/mpg123), Windows (PowerShell)
- **Standalone CLI**: `speak-text` binary for direct TTS without MCP

## Quick Install

```bash
# One-liner installation
curl -fsSL https://raw.githubusercontent.com/ybouhjira/claude-code-tts/main/install.sh | bash
```

Or install manually:

```bash
git clone https://github.com/ybouhjira/claude-code-tts.git ~/.claude/plugins/claude-code-tts
cd ~/.claude/plugins/claude-code-tts
make install
```

## Requirements

- **Go 1.21+** (for building from source)
- **OpenAI API Key** with TTS access
- **Audio Player**:
  - macOS: `afplay` (built-in)
  - Linux: `mpv`, `ffplay`, or `mpg123`
  - Windows: PowerShell (built-in)

## Configuration

Set your OpenAI API key:

```bash
export OPENAI_API_KEY="sk-..."
```

Or add to your shell profile (`~/.zshrc` or `~/.bashrc`).

## Providers & Configuration

### Providers

Three TTS providers are supported:

| Provider | Description | API Key env var |
|----------|-------------|-----------------|
| `openai` | OpenAI TTS API (`tts-1`). Voices: alloy, echo, fable, onyx, nova, shimmer. | `OPENAI_API_KEY` |
| `grok` | xAI Grok TTS API. Voices: eve, leo, and others from xAI. | `XAI_API_KEY` |
| `piper` | Local synthesis via the [Piper](https://github.com/rhasspy/piper) binary. No API key required. **Local playback only** — cannot be used with the relay/companion path. | _(none)_ |

> **API keys come only from environment variables.** They are never stored in the config file.
> The relay/companion path requires OpenAI or Grok (a cloud provider). Piper is local-playback only.

### Config file

Place your config at:

```
~/.claude/plugins/claude-code-tts/config.json
```

Override the path with the `CLAUDE_TTS_CONFIG` environment variable.

If no config file is present, the plugin falls back to the OpenAI default (model `tts-1`, voice `alloy`, speed `1.0`).

Copy `config.example.json` from the repo root to get started:

```bash
cp config.example.json ~/.claude/plugins/claude-code-tts/config.json
```

Example `config.json`:

```json
{
  "default_provider": "openai",
  "default_profile": "default",
  "providers": {
    "openai": { "api_key_env": "OPENAI_API_KEY", "model": "tts-1" },
    "grok":   { "api_key_env": "XAI_API_KEY", "language": "auto" },
    "piper":  { "binary": "piper", "model_dir": "~/.claude/plugins/claude-code-tts/piper" }
  },
  "profiles": {
    "default": { "provider": "openai", "voice": "alloy", "speed": 1.0 },
    "error":   { "provider": "openai", "voice": "onyx",  "speed": 1.0 },
    "offline": { "provider": "piper",  "voice": "en_US-amy-medium" }
  }
}
```

### Named profiles

Profiles are named `(provider, voice, speed, model)` selections defined in `config.json`. The `speak` MCP tool and `speak-text` CLI both accept a `-profile` flag (CLI) or `profile` parameter (MCP tool) to select a profile by name.

### Environment variable overrides

These environment variables override the config file at runtime (no file edit needed):

| Variable | Effect |
|----------|--------|
| `CLAUDE_TTS_CONFIG` | Path to config file (overrides default location) |
| `CLAUDE_TTS_PROFILE` | Use the named profile instead of the configured default |
| `CLAUDE_TTS_PROVIDER` | Use an explicit provider name, bypassing profiles |
| `CLAUDE_TTS_VOICE` | Override the voice for the resolved profile or provider |
| `CLAUDE_TTS_SPEED` | Override the speech speed (float, e.g. `1.2`) |
| `CLAUDE_TTS_MODEL` | Override the model (e.g. `tts-1-hd`) |

When `CLAUDE_TTS_PROVIDER` is set it takes priority and `CLAUDE_TTS_PROFILE` is ignored. All other overrides (`VOICE`, `SPEED`, `MODEL`) apply on top of whichever resolution path is active.

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                     Claude Code                              │
│                         │                                    │
│                    MCP Protocol                              │
│                         │                                    │
│  ┌──────────────────────▼──────────────────────────────┐    │
│  │              TTS MCP Server (Go)                     │    │
│  │  ┌─────────────────────────────────────────────┐    │    │
│  │  │              Tool Handlers                   │    │    │
│  │  │   speak(text, voice)  │  tts_status()       │    │    │
│  │  └─────────────┬─────────┴─────────────────────┘    │    │
│  │                │                                     │    │
│  │  ┌─────────────▼─────────────────────────────┐      │    │
│  │  │           Worker Pool (2 workers)          │      │    │
│  │  │  ┌─────────┐    ┌─────────────────────┐   │      │    │
│  │  │  │ Job     │───►│ Queue (50 slots)    │   │      │    │
│  │  │  │ Submit  │    └──────────┬──────────┘   │      │    │
│  │  │  └─────────┘               │              │      │    │
│  │  │                   ┌────────▼────────┐     │      │    │
│  │  │                   │ Worker 1 │ 2    │     │      │    │
│  │  │                   └────────┬────────┘     │      │    │
│  │  └────────────────────────────│──────────────┘      │    │
│  │                               │                      │    │
│  │  ┌────────────────────────────▼──────────────────┐  │    │
│  │  │              OpenAI TTS API                    │  │    │
│  │  │         POST /v1/audio/speech                  │  │    │
│  │  │         Model: tts-1                           │  │    │
│  │  └───────────────────┬────────────────────────────┘  │    │
│  │                      │                               │    │
│  │  ┌───────────────────▼────────────────────────────┐  │    │
│  │  │         Audio Player (Mutex Protected)          │  │    │
│  │  │   macOS: afplay │ Linux: mpv │ Win: PowerShell  │  │    │
│  │  └─────────────────────────────────────────────────┘  │    │
│  └──────────────────────────────────────────────────────┘    │
└─────────────────────────────────────────────────────────────┘
```

## Usage

### speak(text, ...)

Convert text to speech and play it aloud.

**Parameters:**
| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `text` | string | Yes | Text to speak (max 4096 chars) |
| `profile` | string | No | Named voice profile from config (e.g. `default`, `error`) |
| `provider` | string | No | Explicit provider: `openai`, `grok`, or `piper` |
| `voice` | string | No | Override the profile's voice |
| `speed` | number | No | Override speech speed (provider-dependent range) |

**OpenAI Voices:**
| Voice | Description |
|-------|-------------|
| `alloy` | Neutral, balanced |
| `echo` | Male, warm |
| `fable` | British accent |
| `onyx` | Deep male |
| `nova` | Female, friendly |
| `shimmer` | Soft female |

**Examples:**
```
Use the speak tool to say "Build completed successfully!"
Use the speak tool with profile "error" to say "Build failed"
Use the speak tool with provider "grok" and voice "leo" to say "Done"
```

### tts_status()

Get the current status of the TTS system.

**Returns:**
```json
{
  "worker_count": 2,
  "queue_size": 50,
  "queue_pending": 0,
  "total_processed": 15,
  "total_failed": 0,
  "is_playing": false,
  "recent_jobs": [...]
}
```

## Automatic TTS (Deterministic)

This plugin includes a **Stop hook** that automatically speaks the first sentence of every Claude response. No configuration needed - it just works.

**How it works:**
```
Claude responds → Stop hook fires → First sentence extracted → Audio plays
```

The hook runs in the background and won't block Claude's responses.

### speak-text CLI

A standalone binary for direct TTS without going through MCP:

```bash
# Basic usage
speak-text "Hello world"

# Use a named profile
speak-text -profile error "Build failed"

# Use an explicit provider and voice
speak-text -provider grok -voice leo "Build failed"

# Override speed
speak-text -speed 1.2 "Deploying..."
```

Located at `~/.claude/plugins/claude-code-tts/bin/speak-text` after installation.

## Project Structure

```
claude-code-tts/
├── cmd/
│   ├── tts-server/
│   │   └── main.go           # MCP server entry point
│   └── speak-text/
│       └── main.go           # Standalone CLI binary
├── hooks/
│   └── auto-speak.sh         # Stop hook for deterministic TTS
├── internal/
│   ├── audio/
│   │   └── player.go         # Cross-platform audio playback
│   ├── server/
│   │   ├── server.go         # MCP server & tool handlers
│   │   └── worker.go         # Worker pool implementation
│   └── tts/
│       └── openai.go         # OpenAI TTS client
├── plugin.json                # Plugin metadata + hook config
├── Makefile                   # Build automation
└── install.sh                 # One-liner installer
```

## Building from Source

```bash
# Clone the repository
git clone https://github.com/ybouhjira/claude-code-tts.git
cd claude-code-tts

# Build
make build

# Install to Claude Code plugins
make install

# Run tests
make test
```

## Voice output & Telegram

### Output modes

The plugin supports four voice output modes:

| Mode | Description |
|------|-------------|
| `off` | Silent — speech synthesis is skipped entirely |
| `computer` | Play on the local machine's speakers (default) |
| `phone` | Send to your phone via Telegram (no local playback) |
| `both` | Play locally AND send to Telegram |

The default mode is `computer`.

### Setting the mode from chat

Ask Claude to change the output mode — it will call the `tts_output` MCP tool automatically. Examples:

```
Turn the voice off
Speak out loud
Send the voice to my phone
Use both speakers and phone
```

### Setting the mode from the CLI

Use the `speak-text` binary's `mode` and `status` subcommands:

```bash
# Show the current mode
speak-text mode

# Set a new mode (off / computer / phone / both)
speak-text mode phone
speak-text mode off
speak-text mode both

# Show the current mode and Telegram configuration status
speak-text status
```

### One-off delivery with `-to`

To deliver a single clip to a specific destination without changing the saved mode:

```bash
speak-text -to phone "Deployment complete"
speak-text -to both  "Tests passed"
```

`-to` accepts `computer`, `phone`, or `both` (not `off`).

### Telegram setup

Telegram delivery requires an MP3-capable provider (OpenAI or Grok). Piper produces WAV and cannot be used with Telegram.

1. **Create a Telegram bot** via [@BotFather](https://t.me/BotFather) and note the bot token.

2. **Set the token** in your environment (add to `~/.zshrc` or `~/.bashrc`):

   ```bash
   export TELEGRAM_BOT_TOKEN="123456789:ABCdef..."
   ```

3. **Find your chat id**: Message your bot once (any text), then open:

   ```
   https://api.telegram.org/bot<YOUR_TOKEN>/getUpdates
   ```

   The `"id"` field inside `"chat"` is your chat id.

4. **Add the `telegram` section** to `~/.claude/plugins/claude-code-tts/config.json`:

   ```json
   "telegram": { "bot_token_env": "TELEGRAM_BOT_TOKEN", "chat_id": "YOUR_CHAT_ID" }
   ```

5. Verify the setup:

   ```bash
   speak-text status
   # → voice mode: computer
   # → telegram: configured
   ```

### Interactive bot commands

Once Telegram is configured and Claude Code is open, you can control the voice directly from your phone. The bot only responds while `tts-server` is running (i.e. while Claude Code is open), and it only obeys messages from the configured `chat_id`.

| Command | What it does |
|---------|-------------|
| `/voices` | Sends an audio demo clip for every available voice. Tap the "Use \<voice\>" button under any clip to switch to it immediately. |
| `/model` | Shows a button menu of available models (e.g. `tts-1`, `tts-1-hd`). Tap one to switch. |
| `/menu` | Displays the currently selected voice and model with the model picker. |
| `/help` | Lists the available commands. (Also triggered by `/start`.) |

Selections made via the bot persist in `~/.claude/plugins/claude-code-tts/voice-settings.json` and take effect for every subsequent `speak` call without restarting the server.

### Voice message caption

Every voice message sent to Telegram includes a caption with the estimated cost, the active model, and the active voice:

```
0.02¢ · tts-1 · alloy
```

The cost estimate is based on the character count of the synthesized text and the OpenAI pricing table for the active model.

## Troubleshooting

### "openai: OPENAI_API_KEY is not set" (or "grok: XAI_API_KEY is not set")
The server no longer requires a key at startup — keys are checked per-provider when a clip is synthesized. Set the key for whichever provider your active profile uses:
```bash
export OPENAI_API_KEY="sk-..."   # OpenAI provider
export XAI_API_KEY="xai-..."     # Grok provider
```

### "No suitable audio player found on Linux"
Install one of: `mpv`, `ffplay`, or `mpg123`:
```bash
# Ubuntu/Debian
sudo apt install mpv

# Fedora
sudo dnf install mpv

# Arch
sudo pacman -S mpv
```

### Audio not playing on macOS
Check that `afplay` works:
```bash
# Test with a sample audio file
afplay /System/Library/Sounds/Ping.aiff
```

### Queue is full
The default queue size is 50. If you're hitting this limit:
1. Wait for current jobs to complete
2. Check `tts_status()` to see pending jobs
3. The queue will drain as jobs are processed

### High latency
- OpenAI TTS API typically takes 1-3 seconds per request
- Audio files must download completely before playing
- Consider keeping messages short for faster feedback

## API Costs

This plugin uses OpenAI's `tts-1` model:
- **Cost**: ~$0.015 per 1,000 characters
- **Example**: "Hello, world!" (13 chars) = ~$0.0002

## Contributing

Contributions are welcome! Please see [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

## License

MIT License - see [LICENSE](LICENSE) for details.

## Credits

- [OpenAI TTS API](https://platform.openai.com/docs/guides/text-to-speech)
- [mcp-go](https://github.com/mark3labs/mcp-go) - Go MCP implementation
- [Model Context Protocol](https://modelcontextprotocol.io)
