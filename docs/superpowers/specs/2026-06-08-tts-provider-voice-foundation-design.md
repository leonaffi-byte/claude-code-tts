# Provider & Voice Foundation — Design Spec

- **Date:** 2026-06-08
- **Status:** Approved (design); pending implementation plan
- **Branch:** `feat/provider-voice-foundation`
- **Sub-project:** 1 of 4 (foundation). Later sub-projects — Telegram delivery, cost & caching, smarter "what to speak" — build on this.

## 1. Problem & Motivation

Today the entire synthesis path is hardcoded to OpenAI:

- `internal/tts/openai.go` — `tts.Client` with model `tts-1` hardcoded; `Synthesize(text, voice) ([]byte, error)`.
- `tts.Voice` is a closed enum of six OpenAI voices, used by the MCP `speak` tool, the `speak-text` CLI, the worker pool, and the relay.
- **No** model, **no** speed, **no** provider configurability anywhere.
- The relay/auto-speak path hardcodes `tts.VoiceAlloy`.
- `internal/audio/player.go` always writes a `.mp3` temp file and, on **Windows**, plays it via `Media.SoundPlayer`, which only supports **WAV** — so OpenAI MP3 output does not play correctly on Windows today (latent bug).

We want a foundation that supports multiple TTS providers and configurable voice/model/speed, selected via a config file with named profiles.

## 2. Goals

1. A small **provider abstraction** with three implementations: **OpenAI** (existing, refactored), **Grok/xAI** (new, HTTP), **Piper** (new, local/offline subprocess).
2. **Configuration via a JSON config file** with a default provider, per-provider settings, and named voice **profiles** (`default`, `error`, …). API keys come only from env vars. Env vars override file settings for quick changes.
3. **Format-aware playback** that correctly plays both MP3 (OpenAI/Grok) and WAV (Piper), including fixing Windows MP3 playback.
4. Wire all consumers (worker pool, MCP `speak` tool, `speak-text` CLI, relay/auto-speak) through the abstraction.
5. Make the worker pool's synthesizer/player **injectable** (also closes the test-coverage gap where the real `Synthesize` is never exercised).
6. **Backward compatible:** with no config file and no env vars, behave exactly like today (OpenAI, `alloy`, `tts-1`).

## 3. Non-Goals (explicitly deferred)

- Telegram voice messages / calls (sub-project 2).
- Audio caching and cost controls / spend caps (sub-project 3).
- Smarter "what to speak" — summaries, event-based rules (sub-project 4).
- WAV→MP3 transcoding (would require ffmpeg). Consequence: **Piper is local-playback only in v1** (see §9).
- WebSocket streaming TTS (Grok supports it; not needed here).
- New voices for the relay companion / per-event trigger *logic* (the foundation provides the *capability* via profiles; the trigger logic is sub-project 4).

## 4. Approved Decisions

- **(a)** Piper is **local-playback only** in v1. The relay/companion path requires an MP3-emitting provider (OpenAI/Grok); a WAV provider configured for the relay is rejected at startup with a clear message. No ffmpeg dependency.
- **(b)** Auto-speak/relay uses the **`default` profile** instead of hardcoded `alloy`.
- **(c)** Package split: provider abstraction in **`internal/tts`**, config/registry in **`internal/ttsconfig`**.
- **(d)** `model` is an OpenAI-only concept (`tts-1`/`tts-1-hd`/`gpt-4o-mini-tts`). Grok has no model param; Piper's "model" is the voice file. Profiles carry an optional `model` applied only where relevant.

## 5. Architecture

### 5.1 Provider abstraction (`internal/tts`)

```go
package tts

type Request struct {
    Text  string
    Voice string  // provider-scoped voice id (e.g. "alloy", "eve", or a Piper model name)
    Speed float64 // 0 = provider default; otherwise clamped to provider's range
    Model string  // optional, provider-specific (OpenAI only); "" = provider default
}

type Audio struct {
    Data   []byte
    Format string // "mp3" | "wav"
}

type Provider interface {
    Name() string
    Synthesize(ctx context.Context, req Request) (Audio, error)
    // Voices returns the provider's known voice ids for validation/help text.
    Voices() []string
    // DefaultFormat is the codec this provider emits ("mp3" | "wav").
    DefaultFormat() string
}
```

Implementations:

- **`internal/tts/openai.go` → `OpenAIProvider`** — refactor of the current client. Now `context`-aware (fixes missing ctx propagation), honors `Model` (default `tts-1`) and `Speed`, returns `Audio{Data, "mp3"}`. Endpoint `https://api.openai.com/v1/audio/speech`. Voices: alloy, echo, fable, onyx, nova, shimmer.
- **`internal/tts/grok.go` → `GrokProvider`** — `POST https://api.x.ai/v1/tts`, `Authorization: Bearer $XAI_API_KEY`. Body: `{ "text", "voice_id", "language", "output_format": {"codec":"mp3","sample_rate":24000,"bit_rate":128000}, "speed" }`. Returns raw MP3 bytes → `Audio{Data, "mp3"}`. Voices: eve (default), ara, rex, sal, leo (+ custom ids). `Speed` range 0.7–1.5 (clamped). `language` defaults to `"auto"` (configurable per provider).
- **`internal/tts/piper.go` → `PiperProvider`** — runs the `piper` binary as a subprocess: writes `Text` to stdin, has piper write a temp WAV (`--model <model_dir>/<voice>.onnx --output_file <tmp.wav>`), reads the WAV back → `Audio{Data, "wav"}`. `Speed` maps to `--length-scale` (≈ `1/Speed`). Offline, no API key. "Voice" = a model file name under the configured `model_dir`. Missing binary or model → clear error. (Exact flags verified during implementation.)

A `tts.Voice` shim/constants may be retained for OpenAI for back-compat, but the canonical voice type going forward is a provider-scoped `string`.

### 5.2 Config & registry (`internal/ttsconfig`)

- **Discovery:** load `~/.claude/plugins/claude-code-tts/config.json`, overridable via `CLAUDE_TTS_CONFIG`. A tracked `config.example.json` ships in the repo. Missing file → synthesized default config (back-compat, §8).
- **Schema** (the approved shape):

```json
{
  "default_provider": "grok",
  "default_profile": "default",
  "providers": {
    "openai": { "api_key_env": "OPENAI_API_KEY", "model": "tts-1" },
    "grok":   { "api_key_env": "XAI_API_KEY", "language": "auto" },
    "piper":  { "binary": "piper", "model_dir": "~/.claude/plugins/claude-code-tts/piper" }
  },
  "profiles": {
    "default": { "provider": "grok",  "voice": "eve", "speed": 1.0 },
    "error":   { "provider": "grok",  "voice": "leo", "speed": 1.0 },
    "offline": { "provider": "piper", "voice": "en_US-amy-medium" }
  }
}
```

- **Secrets:** only `api_key_env` (a *variable name*) appears in the file; the key value is read from the environment. Keys are never written to or read from the file.
- **Env overrides** (highest precedence for quick changes): `CLAUDE_TTS_PROFILE`, `CLAUDE_TTS_PROVIDER`, `CLAUDE_TTS_VOICE`, `CLAUDE_TTS_SPEED`, `CLAUDE_TTS_MODEL`, plus `CLAUDE_TTS_CONFIG` (file path).
- **Registry API:**

```go
type Registry struct { /* providers + profiles + defaults */ }

func Load() (*Registry, error)                                  // discovery + validation
func (r *Registry) Resolve(profile string) (tts.Provider, tts.Request, error)
func (r *Registry) ResolveVoice(provider, voice string, speed float64) (tts.Provider, tts.Request, error)
func (r *Registry) Default() (tts.Provider, tts.Request, error) // the default profile
```

- **Validation at load:** unknown `default_provider`/profile provider, unknown voice for a provider, missing required `api_key_env` value, and (for the relay path) a non-MP3 provider where MP3 is required → descriptive errors. Validation failures degrade gracefully where reasonable (e.g. MCP server logs and falls back to the back-compat OpenAI default rather than refusing to start) — exact policy decided in the plan.

### 5.3 Format-aware playback (`internal/audio`)

- New signature: `Play(data []byte, format string) error` (format `"mp3"` | `"wav"`). Temp file extension matches format.
- **Windows:** `format=="wav"` → `Media.SoundPlayer` (native, reliable); `format=="mp3"` → MCI / WPF `MediaPlayer` (so OpenAI/Grok MP3 actually plays). This fixes the current Windows latent bug.
- **macOS:** `afplay` handles both.
- **Linux:** `mpv`/`ffplay` handle both; `mpg123` kept as MP3-only fallback; for WAV with only `aplay` available, use `aplay` directly.
- `IsPlaying()` semantics unchanged for this sub-project (the review's lock-vs-atomic note is out of scope here).

### 5.4 Consumer wiring

- **Worker pool (`internal/server/worker.go`)** — `NewWorkerPool` accepts an injected `*ttsconfig.Registry` and a player (behind a small interface), instead of hardcoding `tts.NewClient()` / `audio.NewPlayer()`. `Job` gains `Provider`/`Profile`/`Speed`/`Format` fields. `processJob` resolves the provider, synthesizes (ctx-aware), and plays with the returned format. This also makes the pool unit-testable with a mock provider + mock player.
- **MCP `speak` tool (`internal/server/server.go`)** — add optional `profile` and `provider` arguments; `voice`/`speed` remain optional. Resolution precedence: explicit tool args > env overrides > default profile. The tool description lists the active/default provider's voices (or available profiles). Replace `tts.IsValidVoice` with provider-scoped validation via the registry.
- **`speak-text` CLI (`cmd/speak-text/main.go`)** — add `-provider`, `-profile`, `-speed` flags alongside `-voice`; resolve via the registry; play with format.
- **Relay / auto-speak (`internal/relay/*`, `cmd/relay/main.go`)** — stop hardcoding `alloy`. `cmd/relay/main.go` loads the registry and resolves the **`default`** profile (or a dedicated `autospeak` profile if present). The relay's `Synthesizer` interface is updated to carry format; the handler asserts MP3 for stored clips. At startup, if the resolved relay profile is a WAV provider (e.g. Piper), fail fast with a clear message (per Decision (a)).

## 6. Data Flow

```
config.json (+ env overrides)
        │  Load()/validate
        ▼
   ttsconfig.Registry ──Resolve(profile)──► (tts.Provider, tts.Request)
        │                                          │ Synthesize(ctx, req)
        ▼                                          ▼
  consumers:                                  tts.Audio{Data, Format}
   - worker pool (MCP speak)  ─────────────►  audio.Play(data, format)
   - speak-text CLI           ─────────────►  audio.Play(data, format)
   - relay /ingest (auto-speak) ───────────►  clip store (MP3 only) → SSE/Push
```

## 7. Error Handling

- Missing/empty API key for the selected provider → clear "set $XAI_API_KEY" style error.
- Piper binary not found / model file missing → actionable error naming the expected path.
- Unknown profile / unknown voice for provider → error listing valid options.
- Provider HTTP failure → wrapped error with status; upstream error bodies truncated before logging (avoid leaking large/echoed payloads).
- Relay configured with a WAV provider → fail fast at startup.
- All synthesis paths are `context`-aware so timeouts/cancellation propagate.

## 8. Backward Compatibility / Migration

- **No config file + no env vars:** synthesize a default registry equivalent to today — provider `openai`, profile `default = {provider: openai, voice: alloy, model: tts-1, speed: 1.0}`, key from `OPENAI_API_KEY`. Existing users keep working unchanged (and Windows MP3 playback gets fixed as a strict improvement).
- Existing `OPENAI_API_KEY` and `CLAUDE_TTS_ENABLED` semantics unchanged.
- `config.example.json` documents the full schema for opt-in Grok/Piper.

## 9. Scope Boundary: Piper & the Relay

Piper emits WAV; the relay clip store, companion PWA, and (later) Telegram expect MP3. To avoid an ffmpeg dependency in v1:

- Piper is fully supported on the **local-playback** paths (MCP `speak`, `speak-text` CLI) where the format-aware player handles WAV.
- The **relay/auto-speak** path requires an MP3 provider. If a WAV provider is configured for the relay profile, the relay fails fast at startup with guidance to use OpenAI/Grok or wait for transcoding support.
- WAV→MP3 transcoding is a documented future enhancement.

## 10. Testing Strategy

- **Real method coverage** (closes the review gap): make provider endpoints injectable (base URL) and exercise the *real* `OpenAIProvider.Synthesize` and `GrokProvider.Synthesize` against `httptest` servers — assert URL, auth header, request body shape, status handling, and returned format.
- **Piper:** test against a fake `piper` executable placed on `PATH` (asserts args/stdin, returns a known WAV); plus a missing-binary error case.
- **Config:** unit tests for load, env override precedence, profile resolution, and validation errors (unknown provider/voice, missing key, WAV-for-relay).
- **Worker pool:** mock provider + mock player; assert job state transitions and that `Format` flows through to `Play`.
- **Relay:** assert non-MP3 provider rejected at startup; existing relay tests updated for the new `Synthesizer` signature.
- **Player:** format → temp-file extension and platform command selection (table test); guard Unix-permission/OS-specific tests with `runtime.GOOS` where needed.

## 11. File Change Map

**New**
- `internal/tts/provider.go` — `Request`, `Audio`, `Provider` interface.
- `internal/tts/grok.go` — `GrokProvider` (+ test).
- `internal/tts/piper.go` — `PiperProvider` (+ test).
- `internal/ttsconfig/config.go` — schema + `Load()` + env overrides (+ test).
- `internal/ttsconfig/registry.go` — `Registry` + resolution + validation (+ test).
- `config.example.json` — documented sample config.

**Modified**
- `internal/tts/openai.go` — refactor to `OpenAIProvider` (ctx, model, speed, `Audio`); keep voice constants.
- `internal/audio/player.go` — `Play(data, format)`; Windows MP3 vs WAV; format-aware temp file.
- `internal/server/worker.go` — injectable registry + player; `Job` format fields; ctx-aware `processJob`.
- `internal/server/server.go` — `speak` tool gains `provider`/`profile`/`speed`; provider-scoped voice validation.
- `cmd/speak-text/main.go` — `-provider`/`-profile`/`-speed` flags.
- `internal/relay/synthesizer.go` + `handler.go` — provider + format; MP3 assertion.
- `cmd/relay/main.go` — load registry; resolve relay profile; WAV-provider fail-fast.
- `Makefile` — ensure new packages build/test; (no new runtime deps for OpenAI/Grok; Piper is an external optional binary).
- Tests across the above.

**Docs (follow-up, can be in the same PR)**
- README: document providers, config file, profiles, `CLAUDE_TTS_*` env vars, and that Piper is local-only.

## 12. Open Questions

None blocking. Exact Piper CLI flags and the precise validation-failure fallback policy (hard error vs fall back to OpenAI default) will be settled in the implementation plan.
