# Telegram → Claude two-way voice control

`claude-tts run` launches the real `claude` CLI inside a pseudo-terminal that it
owns, then runs a background poller that reads your Telegram replies and injects
them into the live session as if you had typed them. Text replies are injected
verbatim; voice notes are transcribed (and, by default, translated to English)
before injection.

The outbound audio path (auto-speak hook, relay, Telegram `Sender`) is unchanged.

## Setup

1. You already have a Telegram bot token and your `chat_id` configured for
   outbound delivery (see the main README). Inbound reuses the same bot and
   `chat_id`.
2. Build the wrapper:

   ```bash
   make build      # produces bin/claude-run (among the other binaries)
   ```

3. Make sure the environment variables are set in the shell where you run
   `claude`:

   - `TELEGRAM_BOT_TOKEN` — the bot token (or whatever env var name you set in
     `telegram.bot_token_env`).
   - `OPENAI_API_KEY` — reused for speech-to-text and translation. If unset,
     text replies still work but voice notes cannot be transcribed.

### Alias `claude` to the wrapper

For a transparent experience, alias `claude` so every interactive session is
wrapped. The wrapper forwards all arguments to the real `claude`.

PowerShell (`$PROFILE`):

```powershell
function claude { & "C:\path\to\bin\claude-run.exe" run @args }
```

Bash / Zsh (`~/.bashrc` / `~/.zshrc`):

```bash
alias claude='/path/to/bin/claude-run run'
```

You can also invoke it directly as `claude-run ...` or `claude-tts run ...`; the
leading `run` token is optional and stripped if present.

## Configuration reference (`telegram.inbound`)

Add an `inbound` block under `telegram` in your config
(`~/.claude/plugins/claude-code-tts/config.json`):

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

| Field | Default | Meaning |
|-------|---------|---------|
| `enabled` | `false` | Master switch for the inbound path. |
| `transcribe_model` | `gpt-4o-mini-transcribe` | OpenAI model used for voice-note transcription. |
| `translate` | `true` | Translate the transcript into `target_language` before injecting. Set to `false` to inject the raw transcript. |
| `source_language` | `auto` | ISO-639-1 hint for transcription; `auto` lets the model detect it. |
| `target_language` | `English` | Language the translation step produces. |
| `require_reply` | `false` | When `true`, only Telegram messages that are an explicit reply to one of Claude's messages are accepted; standalone messages are dropped. |

`translate` is a JSON boolean; omitting it defaults to `true`. The other defaults
apply when the field is empty or absent.

## Security

**Anyone who can message your bot can drive your Claude session.** Treat this as
remote command execution and protect it accordingly:

- **Keep the bot token secret.** It is read only from the environment, never from
  the config file, and is redacted from every log and error message. Do not paste
  it anywhere.
- **Set `chat_id` correctly.** Inbound messages are accepted **only** from the
  configured `chat_id`. Messages from any other chat are dropped and logged. A
  wrong or empty `chat_id` is fail-closed (inbound is skipped).
- **Consider `require_reply: true`** so that only deliberate replies to Claude's
  prompts are injected, reducing the chance of accidental injection.

## Single active session

Only one wrapped session consumes Telegram updates at a time. The first
`claude-run` process acquires a single-flight lock
(`inbound.lock` in the plugin state directory, overridable via
`CLAUDE_TTS_STATE`). Additional concurrent sessions still run `claude` and proxy
your terminal normally, but they do not poll Telegram — they print
`another session owns Telegram inbound; running proxy-only` and continue. When
the owning session exits, the lock is released for the next one.

If a PTY cannot be created at all, the wrapper degrades to running `claude`
directly attached to your terminal; in that mode Telegram inbound is unavailable.
