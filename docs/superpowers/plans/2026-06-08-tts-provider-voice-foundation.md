# TTS Provider & Voice Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the hardcoded OpenAI-only synthesis path with a pluggable provider abstraction (OpenAI, Grok/xAI, Piper), a JSON config file with named voice profiles, and format-aware audio playback.

**Architecture:** A `tts.Provider` interface (`Synthesize(ctx, Request) (Audio, error)`) implemented by three providers. A `ttsconfig.Registry` loads `config.json` (+ env overrides) and resolves named profiles to a `(Provider, Request)` pair. Consumers — the worker pool (MCP `speak`), the `speak-text` CLI, and the relay/auto-speak path — depend on the registry. The audio player becomes format-aware (MP3 vs WAV), fixing Windows MP3 playback. No config + no env behaves exactly like today (OpenAI/alloy/tts-1).

**Tech Stack:** Go 1.23, stdlib `net/http` + `os/exec`, `mcp-go`. New external runtime dependency is the optional `piper` binary (local-only). No new Go modules.

**Spec:** `docs/superpowers/specs/2026-06-08-tts-provider-voice-foundation-design.md`

**Module path:** `github.com/ybouhjira/claude-code-tts` (unchanged).

---

## File Structure

**New files**
- `internal/tts/provider.go` — `Request`, `Audio`, `Provider` interface, `ClampSpeed` helper.
- `internal/tts/grok.go` + `internal/tts/grok_test.go` — `GrokProvider`.
- `internal/tts/piper.go` + `internal/tts/piper_test.go` — `PiperProvider`.
- `internal/ttsconfig/config.go` + `config_test.go` — config structs, `Load`, `DefaultConfig`.
- `internal/ttsconfig/registry.go` + `registry_test.go` — `Registry`, resolution, validation.
- `config.example.json` — documented sample config.

**Modified files**
- `internal/tts/openai.go` (+ `openai_test.go`) — refactor `Client` → `OpenAIProvider` (ctx, model, speed, `Audio`, injectable base URL).
- `internal/audio/player.go` (+ `player_test.go`) — `Play(data, format)`, `buildPlayCommand`, Windows MP3 vs WAV.
- `internal/server/worker.go` (+ `worker_test.go`) — injectable registry + player; `Job` format fields.
- `internal/server/server.go` (+ `server_test.go`) — load registry; `speak` gains `profile`/`voice`/`speed`.
- `cmd/speak-text/main.go` — `-provider`/`-profile`/`-speed` flags via registry.
- `cmd/tts-server/main.go` — drop the hard `OPENAI_API_KEY` requirement (keys are enforced per-provider at resolve time).
- `internal/relay/synthesizer.go`, `internal/relay/handler.go`, `internal/relay/handler_test.go`, `internal/relay/presence_gaps_test.go` — ctx-aware MP3 `Synthesizer`.
- `cmd/relay/main.go` — load registry, resolve relay profile, fail-fast on WAV provider; new `relay.ProviderSynthesizer` (+ test).
- `README.md`, `CLAUDE.md` — document providers/config (final task).

---

## Task 1: Provider abstraction types

**Files:**
- Create: `internal/tts/provider.go`
- Test: `internal/tts/provider_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/tts/provider_test.go
package tts

import "testing"

func TestClampSpeed(t *testing.T) {
	cases := []struct {
		name             string
		speed, min, max  float64
		want             float64
	}{
		{"zero returns default 1.0", 0, 0.7, 1.5, 1.0},
		{"below min clamps to min", 0.1, 0.7, 1.5, 0.7},
		{"above max clamps to max", 9, 0.7, 1.5, 1.5},
		{"within range unchanged", 1.2, 0.7, 1.5, 1.2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ClampSpeed(c.speed, c.min, c.max); got != c.want {
				t.Errorf("ClampSpeed(%v,%v,%v) = %v, want %v", c.speed, c.min, c.max, got, c.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tts/ -run TestClampSpeed -v`
Expected: FAIL — `undefined: ClampSpeed`.

- [ ] **Step 3: Write minimal implementation**

```go
// internal/tts/provider.go
package tts

import "context"

// Request is a provider-agnostic synthesis request.
type Request struct {
	Text  string
	Voice string  // provider-scoped voice id (e.g. "alloy", "eve", or a Piper model name)
	Speed float64 // 0 = provider default; otherwise clamped to the provider's range
	Model string  // optional, provider-specific (OpenAI only); "" = provider default
}

// Audio is synthesized audio plus its container format.
type Audio struct {
	Data   []byte
	Format string // "mp3" | "wav"
}

// Provider converts text to speech.
type Provider interface {
	Name() string
	Voices() []string     // known voice ids; empty means "any" (e.g. Piper)
	DefaultFormat() string // "mp3" | "wav"
	Synthesize(ctx context.Context, req Request) (Audio, error)
}

// ClampSpeed returns speed clamped to [min,max]. A speed of 0 means "use the
// provider default" and returns 1.0.
func ClampSpeed(speed, min, max float64) float64 {
	if speed == 0 {
		return 1.0
	}
	if speed < min {
		return min
	}
	if speed > max {
		return max
	}
	return speed
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/tts/ -run TestClampSpeed -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tts/provider.go internal/tts/provider_test.go
git commit -m "feat(tts): add Provider interface and Request/Audio types"
```

---

## Task 2: Refactor OpenAI client into OpenAIProvider

The current `tts.Client` hardcodes the URL and model, ignores speed, and returns bare bytes. Refactor it to implement `Provider` with an injectable base URL so the *real* method is tested against `httptest` (closing the existing coverage gap where a duplicated helper was tested instead).

**Files:**
- Modify: `internal/tts/openai.go`
- Modify/replace: `internal/tts/openai_test.go`

- [ ] **Step 1: Write the failing test (exercises the REAL Synthesize)**

```go
// internal/tts/openai_test.go
package tts

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAIProvider_Synthesize(t *testing.T) {
	var gotAuth, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("FAKEMP3"))
	}))
	defer srv.Close()

	p := NewOpenAIProvider("sk-test", "tts-1")
	p.baseURL = srv.URL // unexported field; same-package test

	got, err := p.Synthesize(context.Background(), Request{Text: "hello", Voice: "onyx", Speed: 1.2})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if string(got.Data) != "FAKEMP3" {
		t.Errorf("data = %q, want FAKEMP3", got.Data)
	}
	if got.Format != "mp3" {
		t.Errorf("format = %q, want mp3", got.Format)
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("auth = %q, want Bearer sk-test", gotAuth)
	}
	if gotPath != "/v1/audio/speech" {
		t.Errorf("path = %q, want /v1/audio/speech", gotPath)
	}
	if gotBody["model"] != "tts-1" || gotBody["voice"] != "onyx" || gotBody["input"] != "hello" {
		t.Errorf("body = %+v, want model=tts-1 voice=onyx input=hello", gotBody)
	}
	if gotBody["speed"] != 1.2 {
		t.Errorf("speed = %v, want 1.2", gotBody["speed"])
	}
}

func TestOpenAIProvider_Synthesize_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("bad key"))
	}))
	defer srv.Close()

	p := NewOpenAIProvider("sk-test", "tts-1")
	p.baseURL = srv.URL
	if _, err := p.Synthesize(context.Background(), Request{Text: "x", Voice: "alloy"}); err == nil {
		t.Fatal("expected error on 401, got nil")
	}
}

func TestOpenAIProvider_Metadata(t *testing.T) {
	p := NewOpenAIProvider("k", "")
	if p.Name() != "openai" {
		t.Errorf("Name = %q", p.Name())
	}
	if p.DefaultFormat() != "mp3" {
		t.Errorf("DefaultFormat = %q", p.DefaultFormat())
	}
	if len(p.Voices()) != 6 {
		t.Errorf("Voices = %v", p.Voices())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tts/ -run TestOpenAIProvider -v`
Expected: FAIL — `undefined: NewOpenAIProvider`.

- [ ] **Step 3: Replace `internal/tts/openai.go`**

Replace the entire file with:

```go
// internal/tts/openai.go
package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Voice represents an OpenAI TTS voice (kept for back-compat helpers).
type Voice string

const (
	VoiceAlloy   Voice = "alloy"
	VoiceEcho    Voice = "echo"
	VoiceFable   Voice = "fable"
	VoiceOnyx    Voice = "onyx"
	VoiceNova    Voice = "nova"
	VoiceShimmer Voice = "shimmer"
)

func openAIVoices() []string {
	return []string{"alloy", "echo", "fable", "onyx", "nova", "shimmer"}
}

// ValidVoices returns the OpenAI voices (back-compat helper).
func ValidVoices() []Voice {
	return []Voice{VoiceAlloy, VoiceEcho, VoiceFable, VoiceOnyx, VoiceNova, VoiceShimmer}
}

// IsValidVoice reports whether v is a known OpenAI voice (back-compat helper).
func IsValidVoice(v string) bool {
	for _, valid := range openAIVoices() {
		if valid == v {
			return true
		}
	}
	return false
}

// OpenAIProvider synthesizes speech via the OpenAI TTS API.
type OpenAIProvider struct {
	apiKey     string
	model      string
	baseURL    string
	httpClient *http.Client
}

// NewOpenAIProvider creates a provider. model "" defaults to "tts-1".
func NewOpenAIProvider(apiKey, model string) *OpenAIProvider {
	if model == "" {
		model = "tts-1"
	}
	return &OpenAIProvider{
		apiKey:     apiKey,
		model:      model,
		baseURL:    "https://api.openai.com",
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

func (p *OpenAIProvider) Name() string          { return "openai" }
func (p *OpenAIProvider) Voices() []string       { return openAIVoices() }
func (p *OpenAIProvider) DefaultFormat() string  { return "mp3" }

type openAIRequest struct {
	Model string  `json:"model"`
	Input string  `json:"input"`
	Voice string  `json:"voice"`
	Speed float64 `json:"speed,omitempty"`
}

// Synthesize converts text to MP3 audio.
func (p *OpenAIProvider) Synthesize(ctx context.Context, req Request) (Audio, error) {
	if p.apiKey == "" {
		return Audio{}, fmt.Errorf("openai: OPENAI_API_KEY is not set")
	}
	model := p.model
	if req.Model != "" {
		model = req.Model
	}
	body := openAIRequest{
		Model: model,
		Input: req.Text,
		Voice: req.Voice,
		Speed: ClampSpeed(req.Speed, 0.25, 4.0),
	}
	data, err := json.Marshal(body)
	if err != nil {
		return Audio{}, fmt.Errorf("openai: marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/audio/speech", bytes.NewReader(data))
	if err != nil {
		return Audio{}, fmt.Errorf("openai: create request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return Audio{}, fmt.Errorf("openai: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return Audio{}, fmt.Errorf("openai: API error (status %d): %s", resp.StatusCode, string(errBody))
	}
	audio, err := io.ReadAll(resp.Body)
	if err != nil {
		return Audio{}, fmt.Errorf("openai: read response: %w", err)
	}
	return Audio{Data: audio, Format: "mp3"}, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/tts/ -run 'TestOpenAIProvider|TestClampSpeed' -v`
Expected: PASS. (Build of dependents will break until later tasks — that's expected; we run only this package.)

- [ ] **Step 5: Commit**

```bash
git add internal/tts/openai.go internal/tts/openai_test.go
git commit -m "refactor(tts): make OpenAI an injectable Provider with real-method tests"
```

---

## Task 3: GrokProvider

**Files:**
- Create: `internal/tts/grok.go`
- Test: `internal/tts/grok_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/tts/grok_test.go
package tts

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGrokProvider_Synthesize(t *testing.T) {
	var gotAuth, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		_, _ = w.Write([]byte("GROKMP3"))
	}))
	defer srv.Close()

	p := NewGrokProvider("xai-test", "en")
	p.baseURL = srv.URL

	got, err := p.Synthesize(context.Background(), Request{Text: "hi", Voice: "leo", Speed: 1.3})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if string(got.Data) != "GROKMP3" || got.Format != "mp3" {
		t.Errorf("got %q/%q", got.Data, got.Format)
	}
	if gotAuth != "Bearer xai-test" {
		t.Errorf("auth = %q", gotAuth)
	}
	if gotPath != "/v1/tts" {
		t.Errorf("path = %q, want /v1/tts", gotPath)
	}
	if gotBody["text"] != "hi" || gotBody["voice_id"] != "leo" || gotBody["language"] != "en" {
		t.Errorf("body = %+v", gotBody)
	}
	if gotBody["speed"] != 1.3 {
		t.Errorf("speed = %v, want 1.3", gotBody["speed"])
	}
	of, ok := gotBody["output_format"].(map[string]any)
	if !ok || of["codec"] != "mp3" {
		t.Errorf("output_format = %v, want codec mp3", gotBody["output_format"])
	}
}

func TestGrokProvider_Metadata(t *testing.T) {
	p := NewGrokProvider("k", "")
	if p.Name() != "grok" || p.DefaultFormat() != "mp3" {
		t.Errorf("metadata wrong: %q/%q", p.Name(), p.DefaultFormat())
	}
	if len(p.Voices()) != 5 {
		t.Errorf("voices = %v", p.Voices())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tts/ -run TestGrokProvider -v`
Expected: FAIL — `undefined: NewGrokProvider`.

- [ ] **Step 3: Write implementation**

```go
// internal/tts/grok.go
package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// GrokProvider synthesizes speech via the xAI Grok TTS API.
type GrokProvider struct {
	apiKey     string
	language   string
	baseURL    string
	httpClient *http.Client
}

// NewGrokProvider creates a provider. language "" defaults to "auto".
func NewGrokProvider(apiKey, language string) *GrokProvider {
	if language == "" {
		language = "auto"
	}
	return &GrokProvider{
		apiKey:     apiKey,
		language:   language,
		baseURL:    "https://api.x.ai",
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

func (p *GrokProvider) Name() string         { return "grok" }
func (p *GrokProvider) Voices() []string      { return []string{"eve", "ara", "rex", "sal", "leo"} }
func (p *GrokProvider) DefaultFormat() string { return "mp3" }

type grokOutputFormat struct {
	Codec      string `json:"codec"`
	SampleRate int    `json:"sample_rate"`
	BitRate    int    `json:"bit_rate"`
}

type grokRequest struct {
	Text         string           `json:"text"`
	VoiceID      string           `json:"voice_id"`
	Language     string           `json:"language"`
	OutputFormat grokOutputFormat `json:"output_format"`
	Speed        float64          `json:"speed"`
}

// Synthesize converts text to MP3 audio.
func (p *GrokProvider) Synthesize(ctx context.Context, req Request) (Audio, error) {
	if p.apiKey == "" {
		return Audio{}, fmt.Errorf("grok: XAI_API_KEY is not set")
	}
	voice := req.Voice
	if voice == "" {
		voice = "eve"
	}
	body := grokRequest{
		Text:     req.Text,
		VoiceID:  voice,
		Language: p.language,
		OutputFormat: grokOutputFormat{
			Codec:      "mp3",
			SampleRate: 24000,
			BitRate:    128000,
		},
		Speed: ClampSpeed(req.Speed, 0.7, 1.5),
	}
	data, err := json.Marshal(body)
	if err != nil {
		return Audio{}, fmt.Errorf("grok: marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/tts", bytes.NewReader(data))
	if err != nil {
		return Audio{}, fmt.Errorf("grok: create request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return Audio{}, fmt.Errorf("grok: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return Audio{}, fmt.Errorf("grok: API error (status %d): %s", resp.StatusCode, string(errBody))
	}
	audio, err := io.ReadAll(resp.Body)
	if err != nil {
		return Audio{}, fmt.Errorf("grok: read response: %w", err)
	}
	return Audio{Data: audio, Format: "mp3"}, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/tts/ -run TestGrokProvider -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tts/grok.go internal/tts/grok_test.go
git commit -m "feat(tts): add Grok (xAI) TTS provider"
```

---

## Task 4: PiperProvider (local subprocess)

Piper is invoked as `piper --model <modelDir>/<voice>.onnx --output_file <tmp.wav> --length_scale <ls>` with text on stdin. We mock the binary in tests with the standard Go `TestHelperProcess` pattern via a package-level `execCommand` indirection.

**Files:**
- Create: `internal/tts/piper.go`
- Test: `internal/tts/piper_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/tts/piper_test.go
package tts

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeExecCommand re-invokes the test binary as the "piper" process.
func fakeExecCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	cs := append([]string{"-test.run=TestPiperHelperProcess", "--", name}, args...)
	cmd := exec.CommandContext(ctx, os.Args[0], cs...)
	cmd.Env = append(os.Environ(), "GO_WANT_PIPER_HELPER=1")
	return cmd
}

// TestPiperHelperProcess is not a real test; it emulates the piper binary:
// it finds --output_file <path> and writes fake WAV bytes there.
func TestPiperHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_PIPER_HELPER") != "1" {
		return
	}
	args := os.Args
	for i := len(args) - 1; i >= 0; i-- {
		if args[i] == "--" {
			args = args[i+1:]
			break
		}
	}
	var out string
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--output_file" {
			out = args[i+1]
		}
	}
	if out == "" {
		os.Exit(2)
	}
	_ = os.WriteFile(out, []byte("RIFFfakeWAV"), 0o600)
	os.Exit(0)
}

func TestPiperProvider_Synthesize(t *testing.T) {
	execCommand = fakeExecCommand
	defer func() { execCommand = exec.CommandContext }()

	// The provider stats the model file before running piper, so create a
	// dummy model in the same dir we pass as modelDir (t.TempDir() returns a
	// fresh dir each call — capture it once).
	modelDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(modelDir, "en_US-amy-medium.onnx"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	p := NewPiperProvider("piper", modelDir)
	got, err := p.Synthesize(context.Background(), Request{Text: "hello", Voice: "en_US-amy-medium", Speed: 1.0})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if !strings.HasPrefix(string(got.Data), "RIFF") {
		t.Errorf("data = %q, want WAV", got.Data)
	}
	if got.Format != "wav" {
		t.Errorf("format = %q, want wav", got.Format)
	}
}

func TestPiperProvider_Metadata(t *testing.T) {
	p := NewPiperProvider("piper", "/models")
	if p.Name() != "piper" || p.DefaultFormat() != "wav" {
		t.Errorf("metadata wrong: %q/%q", p.Name(), p.DefaultFormat())
	}
	if len(p.Voices()) != 0 {
		t.Errorf("Voices should be empty (any), got %v", p.Voices())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tts/ -run TestPiperProvider -v`
Expected: FAIL — `undefined: NewPiperProvider` / `execCommand`.

- [ ] **Step 3: Write implementation**

```go
// internal/tts/piper.go
package tts

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// execCommand is indirected so tests can substitute a fake process.
var execCommand = exec.CommandContext

// PiperProvider synthesizes speech offline via the local piper binary.
type PiperProvider struct {
	binary   string
	modelDir string
}

// NewPiperProvider creates a provider. binary "" defaults to "piper".
func NewPiperProvider(binary, modelDir string) *PiperProvider {
	if binary == "" {
		binary = "piper"
	}
	return &PiperProvider{binary: binary, modelDir: expandHome(modelDir)}
}

func (p *PiperProvider) Name() string         { return "piper" }
func (p *PiperProvider) Voices() []string      { return nil } // any installed model
func (p *PiperProvider) DefaultFormat() string { return "wav" }

// Synthesize runs piper, writing WAV to a temp file and returning its bytes.
func (p *PiperProvider) Synthesize(ctx context.Context, req Request) (Audio, error) {
	if req.Voice == "" {
		return Audio{}, fmt.Errorf("piper: a voice (model name) is required")
	}
	model := req.Voice
	if !strings.HasSuffix(model, ".onnx") {
		model += ".onnx"
	}
	if !filepath.IsAbs(model) {
		model = filepath.Join(p.modelDir, model)
	}
	if _, err := os.Stat(model); err != nil {
		return Audio{}, fmt.Errorf("piper: model not found at %s: %w", model, err)
	}

	tmp, err := os.CreateTemp("", "piper-*.wav")
	if err != nil {
		return Audio{}, fmt.Errorf("piper: temp file: %w", err)
	}
	tmp.Close()
	defer os.Remove(tmp.Name())

	// length_scale is inversely proportional to speed (1.0 = normal).
	speed := ClampSpeed(req.Speed, 0.5, 2.0)
	lengthScale := strconv.FormatFloat(1.0/speed, 'f', 3, 64)

	cmd := execCommand(ctx, p.binary,
		"--model", model,
		"--output_file", tmp.Name(),
		"--length_scale", lengthScale,
	)
	cmd.Stdin = strings.NewReader(req.Text)
	if out, err := cmd.CombinedOutput(); err != nil {
		return Audio{}, fmt.Errorf("piper: run failed: %w (%s)", err, strings.TrimSpace(string(out)))
	}

	data, err := os.ReadFile(tmp.Name())
	if err != nil {
		return Audio{}, fmt.Errorf("piper: read output: %w", err)
	}
	return Audio{Data: data, Format: "wav"}, nil
}

func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/tts/ -run 'TestPiperProvider|TestPiperHelperProcess' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tts/piper.go internal/tts/piper_test.go
git commit -m "feat(tts): add offline Piper provider"
```

---

## Task 5: Config loading

**Files:**
- Create: `internal/ttsconfig/config.go`
- Test: `internal/ttsconfig/config_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/ttsconfig/config_test.go
package ttsconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_MissingFileReturnsDefault(t *testing.T) {
	t.Setenv("CLAUDE_TTS_CONFIG", filepath.Join(t.TempDir(), "nope.json"))
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DefaultProvider != "openai" {
		t.Errorf("default provider = %q, want openai", cfg.DefaultProvider)
	}
	if _, ok := cfg.Profiles["default"]; !ok {
		t.Errorf("expected a 'default' profile")
	}
}

func TestLoad_ReadsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	_ = os.WriteFile(path, []byte(`{
      "default_provider": "grok",
      "providers": {"grok": {"api_key_env": "XAI_API_KEY"}},
      "profiles": {"default": {"provider": "grok", "voice": "eve", "speed": 1.1}}
    }`), 0o600)
	t.Setenv("CLAUDE_TTS_CONFIG", path)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DefaultProvider != "grok" {
		t.Errorf("provider = %q", cfg.DefaultProvider)
	}
	if cfg.Profiles["default"].Voice != "eve" || cfg.Profiles["default"].Speed != 1.1 {
		t.Errorf("profile = %+v", cfg.Profiles["default"])
	}
}

func TestLoad_MalformedFileErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	_ = os.WriteFile(path, []byte("{not json"), 0o600)
	t.Setenv("CLAUDE_TTS_CONFIG", path)
	if _, err := Load(); err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/ttsconfig/ -run TestLoad -v`
Expected: FAIL — package/`Load` undefined.

- [ ] **Step 3: Write implementation**

```go
// internal/ttsconfig/config.go
package ttsconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ProviderConfig is per-provider configuration from the config file.
type ProviderConfig struct {
	APIKeyEnv string `json:"api_key_env"`
	Model     string `json:"model"`     // openai
	Language  string `json:"language"`  // grok
	Binary    string `json:"binary"`    // piper
	ModelDir  string `json:"model_dir"` // piper
}

// Profile is a named (provider, voice, speed, model) selection.
type Profile struct {
	Provider string  `json:"provider"`
	Voice    string  `json:"voice"`
	Speed    float64 `json:"speed"`
	Model    string  `json:"model"`
}

// Config is the on-disk configuration.
type Config struct {
	DefaultProvider string                    `json:"default_provider"`
	DefaultProfile  string                    `json:"default_profile"`
	Providers       map[string]ProviderConfig `json:"providers"`
	Profiles        map[string]Profile        `json:"profiles"`
}

// DefaultConfig is the zero-config, back-compatible OpenAI setup.
func DefaultConfig() *Config {
	return &Config{
		DefaultProvider: "openai",
		DefaultProfile:  "default",
		Providers: map[string]ProviderConfig{
			"openai": {APIKeyEnv: "OPENAI_API_KEY", Model: "tts-1"},
		},
		Profiles: map[string]Profile{
			"default": {Provider: "openai", Voice: "alloy", Speed: 1.0},
		},
	}
}

// configPath returns CLAUDE_TTS_CONFIG or the default plugin location.
func configPath() string {
	if p := os.Getenv("CLAUDE_TTS_CONFIG"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "plugins", "claude-code-tts", "config.json")
}

// Load reads the config file, falling back to DefaultConfig when it is absent.
// A present-but-malformed file is an error.
func Load() (*Config, error) {
	path := configPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultConfig(), nil
		}
		return nil, fmt.Errorf("ttsconfig: read %s: %w", path, err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("ttsconfig: parse %s: %w", path, err)
	}
	if cfg.DefaultProfile == "" {
		cfg.DefaultProfile = "default"
	}
	return &cfg, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/ttsconfig/ -run TestLoad -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/ttsconfig/config.go internal/ttsconfig/config_test.go
git commit -m "feat(ttsconfig): load config.json with back-compat default"
```

---

## Task 6: Registry — build providers, resolve profiles, env overrides, validation

**Files:**
- Create: `internal/ttsconfig/registry.go`
- Test: `internal/ttsconfig/registry_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/ttsconfig/registry_test.go
package ttsconfig

import "testing"

func testConfig() *Config {
	return &Config{
		DefaultProvider: "grok",
		DefaultProfile:  "default",
		Providers: map[string]ProviderConfig{
			"openai": {APIKeyEnv: "OPENAI_API_KEY", Model: "tts-1"},
			"grok":   {APIKeyEnv: "XAI_API_KEY"},
			"piper":  {Binary: "piper", ModelDir: "/models"},
		},
		Profiles: map[string]Profile{
			"default": {Provider: "grok", Voice: "eve", Speed: 1.1},
			"offline": {Provider: "piper", Voice: "en_US-amy-medium"},
			"bogus":   {Provider: "nope", Voice: "x"},
		},
	}
}

func TestRegistry_Resolve(t *testing.T) {
	t.Setenv("XAI_API_KEY", "xai-k")
	reg, err := NewRegistry(testConfig())
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	prov, req, err := reg.Resolve("default")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if prov.Name() != "grok" || req.Voice != "eve" || req.Speed != 1.1 {
		t.Errorf("got %s/%+v", prov.Name(), req)
	}
}

func TestRegistry_ResolveUnknownProfile(t *testing.T) {
	t.Setenv("XAI_API_KEY", "xai-k")
	reg, _ := NewRegistry(testConfig())
	if _, _, err := reg.Resolve("missing"); err == nil {
		t.Fatal("expected error for unknown profile")
	}
}

func TestRegistry_ResolveUnknownProviderInProfile(t *testing.T) {
	t.Setenv("XAI_API_KEY", "xai-k")
	reg, _ := NewRegistry(testConfig())
	if _, _, err := reg.Resolve("bogus"); err == nil {
		t.Fatal("expected error for profile referencing unknown provider")
	}
}

func TestRegistry_ResolveMissingKey(t *testing.T) {
	t.Setenv("XAI_API_KEY", "") // unset
	reg, _ := NewRegistry(testConfig())
	if _, _, err := reg.Resolve("default"); err == nil {
		t.Fatal("expected error when XAI_API_KEY missing")
	}
}

func TestRegistry_ResolveInvalidVoice(t *testing.T) {
	t.Setenv("XAI_API_KEY", "xai-k")
	cfg := testConfig()
	cfg.Profiles["bad"] = Profile{Provider: "grok", Voice: "not-a-voice"}
	reg, _ := NewRegistry(cfg)
	if _, _, err := reg.Resolve("bad"); err == nil {
		t.Fatal("expected error for invalid grok voice")
	}
}

func TestRegistry_DefaultWithEnvOverrides(t *testing.T) {
	t.Setenv("XAI_API_KEY", "xai-k")
	t.Setenv("CLAUDE_TTS_VOICE", "leo")
	t.Setenv("CLAUDE_TTS_SPEED", "1.4")
	reg, _ := NewRegistry(testConfig())
	prov, req, err := reg.Default()
	if err != nil {
		t.Fatalf("Default: %v", err)
	}
	if prov.Name() != "grok" || req.Voice != "leo" || req.Speed != 1.4 {
		t.Errorf("override failed: %s/%+v", prov.Name(), req)
	}
}

func TestRegistry_ResolveVoice(t *testing.T) {
	t.Setenv("XAI_API_KEY", "xai-k")
	reg, _ := NewRegistry(testConfig())

	prov, req, err := reg.ResolveVoice("grok", "", 1.0) // empty -> first voice (eve)
	if err != nil {
		t.Fatalf("ResolveVoice: %v", err)
	}
	if prov.Name() != "grok" || req.Voice != "eve" {
		t.Errorf("got %s/%q, want grok/eve", prov.Name(), req.Voice)
	}
	if _, _, err := reg.ResolveVoice("grok", "bogus", 1.0); err == nil {
		t.Error("expected invalid-voice error")
	}
	if _, _, err := reg.ResolveVoice("nope", "x", 1.0); err == nil {
		t.Error("expected unknown-provider error")
	}
}

func TestRegistry_DefaultProviderEnvOverride(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-k")
	t.Setenv("XAI_API_KEY", "xai-k")
	t.Setenv("CLAUDE_TTS_PROVIDER", "openai")
	t.Setenv("CLAUDE_TTS_VOICE", "onyx")
	reg, _ := NewRegistry(testConfig())
	prov, req, err := reg.Default()
	if err != nil {
		t.Fatalf("Default: %v", err)
	}
	if prov.Name() != "openai" || req.Voice != "onyx" {
		t.Errorf("got %s/%q, want openai/onyx", prov.Name(), req.Voice)
	}
}
```

> **Validation policy (intentional):** validation happens lazily at `Resolve`/`ResolveVoice` time (unknown profile/provider, invalid voice, missing key), not at `NewRegistry` time. This satisfies the spec's "degrade gracefully" allowance (§5.2) — a config that lists a provider without its key still lets *other* providers work; the error surfaces only when that provider is actually used.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/ttsconfig/ -run TestRegistry -v`
Expected: FAIL — `undefined: NewRegistry`.

- [ ] **Step 3: Write implementation**

```go
// internal/ttsconfig/registry.go
package ttsconfig

import (
	"fmt"
	"os"
	"strconv"

	"github.com/ybouhjira/claude-code-tts/internal/tts"
)

// Registry holds instantiated providers and resolves profiles to requests.
type Registry struct {
	cfg       *Config
	providers map[string]tts.Provider
	missing   map[string]string // provider name -> missing-key error message
}

// NewRegistry instantiates providers declared in cfg.Providers.
func NewRegistry(cfg *Config) (*Registry, error) {
	r := &Registry{cfg: cfg, providers: map[string]tts.Provider{}, missing: map[string]string{}}
	for name, pc := range cfg.Providers {
		switch name {
		case "openai":
			key := os.Getenv(envOr(pc.APIKeyEnv, "OPENAI_API_KEY"))
			if key == "" {
				r.missing[name] = fmt.Sprintf("set $%s", envOr(pc.APIKeyEnv, "OPENAI_API_KEY"))
			}
			r.providers[name] = tts.NewOpenAIProvider(key, pc.Model)
		case "grok":
			key := os.Getenv(envOr(pc.APIKeyEnv, "XAI_API_KEY"))
			if key == "" {
				r.missing[name] = fmt.Sprintf("set $%s", envOr(pc.APIKeyEnv, "XAI_API_KEY"))
			}
			r.providers[name] = tts.NewGrokProvider(key, pc.Language)
		case "piper":
			r.providers[name] = tts.NewPiperProvider(pc.Binary, pc.ModelDir)
		default:
			return nil, fmt.Errorf("ttsconfig: unknown provider type %q", name)
		}
	}
	return r, nil
}

// Load reads config and builds a registry.
func Load() (*Registry, error)  { return loadRegistry() } // see note below

func loadRegistry() (*Registry, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	return NewRegistry(cfg)
}

// LoadOrDefault builds a registry, falling back to the OpenAI default on error.
func LoadOrDefault() *Registry {
	reg, err := loadRegistry()
	if err != nil {
		reg, _ = NewRegistry(DefaultConfig())
	}
	return reg
}

// Resolve maps a profile name to its provider and a base Request (no Text).
func (r *Registry) Resolve(profile string) (tts.Provider, tts.Request, error) {
	pr, ok := r.cfg.Profiles[profile]
	if !ok {
		return nil, tts.Request{}, fmt.Errorf("ttsconfig: unknown profile %q", profile)
	}
	prov, ok := r.providers[pr.Provider]
	if !ok {
		return nil, tts.Request{}, fmt.Errorf("ttsconfig: profile %q references unknown provider %q", profile, pr.Provider)
	}
	if msg, bad := r.missing[pr.Provider]; bad {
		return nil, tts.Request{}, fmt.Errorf("ttsconfig: provider %q not ready: %s", pr.Provider, msg)
	}
	if voices := prov.Voices(); len(voices) > 0 && pr.Voice != "" && !contains(voices, pr.Voice) {
		return nil, tts.Request{}, fmt.Errorf("ttsconfig: voice %q is not valid for provider %q (valid: %v)", pr.Voice, pr.Provider, voices)
	}
	return prov, tts.Request{Voice: pr.Voice, Speed: pr.Speed, Model: pr.Model}, nil
}

// ResolveVoice resolves an explicit provider + voice (+ speed), bypassing
// profiles. An empty voice uses the provider's first listed voice; providers
// with no fixed voice list (Piper) require an explicit voice.
func (r *Registry) ResolveVoice(provider, voice string, speed float64) (tts.Provider, tts.Request, error) {
	prov, ok := r.providers[provider]
	if !ok {
		return nil, tts.Request{}, fmt.Errorf("ttsconfig: unknown provider %q", provider)
	}
	if msg, bad := r.missing[provider]; bad {
		return nil, tts.Request{}, fmt.Errorf("ttsconfig: provider %q not ready: %s", provider, msg)
	}
	voices := prov.Voices()
	if voice == "" {
		if len(voices) == 0 {
			return nil, tts.Request{}, fmt.Errorf("ttsconfig: provider %q requires an explicit voice", provider)
		}
		voice = voices[0]
	} else if len(voices) > 0 && !contains(voices, voice) {
		return nil, tts.Request{}, fmt.Errorf("ttsconfig: voice %q is not valid for provider %q (valid: %v)", voice, provider, voices)
	}
	return prov, tts.Request{Voice: voice, Speed: speed}, nil
}

// Default resolves the configured default selection, applying CLAUDE_TTS_* env
// overrides. CLAUDE_TTS_PROVIDER (if set) switches to explicit provider+voice
// resolution; otherwise the default profile is used with field overrides.
func (r *Registry) Default() (tts.Provider, tts.Request, error) {
	if pv := os.Getenv("CLAUDE_TTS_PROVIDER"); pv != "" {
		var speed float64
		if s := os.Getenv("CLAUDE_TTS_SPEED"); s != "" {
			if f, err := strconv.ParseFloat(s, 64); err == nil {
				speed = f
			}
		}
		prov, req, err := r.ResolveVoice(pv, os.Getenv("CLAUDE_TTS_VOICE"), speed)
		if err != nil {
			return nil, tts.Request{}, err
		}
		if m := os.Getenv("CLAUDE_TTS_MODEL"); m != "" {
			req.Model = m
		}
		return prov, req, nil
	}

	profile := r.cfg.DefaultProfile
	if p := os.Getenv("CLAUDE_TTS_PROFILE"); p != "" {
		profile = p
	}
	prov, req, err := r.Resolve(profile)
	if err != nil {
		return nil, tts.Request{}, err
	}
	if v := os.Getenv("CLAUDE_TTS_VOICE"); v != "" {
		req.Voice = v
	}
	if m := os.Getenv("CLAUDE_TTS_MODEL"); m != "" {
		req.Model = m
	}
	if s := os.Getenv("CLAUDE_TTS_SPEED"); s != "" {
		if f, perr := strconv.ParseFloat(s, 64); perr == nil {
			req.Speed = f
		}
	}
	return prov, req, nil
}

func envOr(name, fallback string) string {
	if name != "" {
		return name
	}
	return fallback
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
```

> **Note (naming):** Task 5 already defined `Load() (*Config, error)`. Rename that to `loadConfig()` and have `Load() (*Registry, error)` here be the public entry. **Do this in Step 3:** in `config.go` rename `func Load()` → `func loadConfig()`, and update `config_test.go` to call `loadConfig()`. (The registry tests use `NewRegistry` directly, so they are unaffected.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/ttsconfig/ -v`
Expected: PASS (both config and registry tests, after the `Load`→`loadConfig` rename).

- [ ] **Step 5: Commit**

```bash
git add internal/ttsconfig/registry.go internal/ttsconfig/registry_test.go internal/ttsconfig/config.go internal/ttsconfig/config_test.go
git commit -m "feat(ttsconfig): registry with profile resolution, env overrides, validation"
```

---

## Task 7: Format-aware audio player

**Files:**
- Modify: `internal/audio/player.go`
- Modify: `internal/audio/player_test.go` (add the new test; keep existing where still valid)

- [ ] **Step 1: Write the failing test**

```go
// internal/audio/player_test.go  (add this test)
package audio

import (
	"strings"
	"testing"
)

func TestBuildPlayCommand(t *testing.T) {
	cases := []struct {
		goos, format string
		wantContains string // substring expected somewhere in name+args
		wantErr      bool
	}{
		{"windows", "wav", "SoundPlayer", false},
		{"windows", "mp3", "MediaPlayer", false},
		{"darwin", "mp3", "afplay", false},
		{"darwin", "wav", "afplay", false},
		{"plan9", "mp3", "", true},
	}
	for _, c := range cases {
		t.Run(c.goos+"_"+c.format, func(t *testing.T) {
			cmd, err := buildPlayCommand(c.goos, c.format, "/tmp/x."+c.format)
			if c.wantErr {
				if err == nil {
					t.Fatalf("want error for %s/%s", c.goos, c.format)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			joined := cmd.Path + " " + strings.Join(cmd.Args, " ")
			if !strings.Contains(joined, c.wantContains) {
				t.Errorf("cmd %q missing %q", joined, c.wantContains)
			}
		})
	}
}

func TestExtForFormat(t *testing.T) {
	if extForFormat("wav") != ".wav" || extForFormat("mp3") != ".mp3" || extForFormat("") != ".mp3" {
		t.Error("extForFormat mapping wrong")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/audio/ -run 'TestBuildPlayCommand|TestExtForFormat' -v`
Expected: FAIL — `undefined: buildPlayCommand`.

- [ ] **Step 3: Replace `internal/audio/player.go`**

```go
// internal/audio/player.go
package audio

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sync"
)

// Player handles audio playback with mutex protection.
type Player struct {
	mu        sync.Mutex
	isPlaying bool
}

// NewPlayer creates a new audio player.
func NewPlayer() *Player { return &Player{} }

func extForFormat(format string) string {
	if format == "wav" {
		return ".wav"
	}
	return ".mp3"
}

// buildPlayCommand returns the platform command to play a file of the given
// format. Separated out so it is unit-testable without real playback.
func buildPlayCommand(goos, format, path string) (*exec.Cmd, error) {
	switch goos {
	case "darwin":
		return exec.Command("afplay", path), nil
	case "linux":
		if _, err := exec.LookPath("mpv"); err == nil {
			return exec.Command("mpv", "--no-video", path), nil
		}
		if _, err := exec.LookPath("ffplay"); err == nil {
			return exec.Command("ffplay", "-nodisp", "-autoexit", path), nil
		}
		if format == "wav" {
			if _, err := exec.LookPath("aplay"); err == nil {
				return exec.Command("aplay", "-q", path), nil
			}
		} else {
			if _, err := exec.LookPath("mpg123"); err == nil {
				return exec.Command("mpg123", "-q", path), nil
			}
		}
		return nil, fmt.Errorf("no suitable audio player found on Linux (install mpv, ffplay, mpg123, or aplay)")
	case "windows":
		if format == "wav" {
			// SoundPlayer plays WAV natively.
			return exec.Command("powershell", "-NoProfile", "-Command",
				fmt.Sprintf("(New-Object Media.SoundPlayer '%s').PlaySync()", path)), nil
		}
		// MediaPlayer (WPF) handles MP3, which SoundPlayer cannot. The wait for
		// NaturalDuration is BOUNDED (max ~3s) so an unloadable/invalid file can
		// never hang playback — without the bound, a file that never reports a
		// duration would spin forever.
		ps := fmt.Sprintf(
			"Add-Type -AssemblyName PresentationCore; "+
				"$p = New-Object System.Windows.Media.MediaPlayer; "+
				"$p.Open([uri]'%s'); $p.Play(); "+
				"$n = 0; while (-not $p.NaturalDuration.HasTimeSpan -and $n -lt 60) { Start-Sleep -Milliseconds 50; $n++ }; "+
				"if ($p.NaturalDuration.HasTimeSpan) { Start-Sleep -Seconds ([int][math]::Ceiling($p.NaturalDuration.TimeSpan.TotalSeconds) + 1) } else { Start-Sleep -Milliseconds 200 }; "+
				"$p.Close()",
			path)
		return exec.Command("powershell", "-NoProfile", "-Command", ps), nil
	default:
		return nil, fmt.Errorf("unsupported platform: %s", goos)
	}
}

// Play writes audioData to a temp file of the given format and plays it.
// Only one audio plays at a time (mutex protected). format is "mp3" or "wav".
func (p *Player) Play(audioData []byte, format string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.isPlaying = true
	defer func() { p.isPlaying = false }()

	tmpFile, err := os.CreateTemp("", "tts-*"+extForFormat(format))
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.Write(audioData); err != nil {
		tmpFile.Close()
		return fmt.Errorf("failed to write audio data: %w", err)
	}
	tmpFile.Close()

	cmd, err := buildPlayCommand(runtime.GOOS, format, tmpFile.Name())
	if err != nil {
		return err
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("audio playback failed: %w", err)
	}
	return nil
}

// IsPlaying returns whether audio is currently playing.
func (p *Player) IsPlaying() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.isPlaying
}
```

- [ ] **Step 4: Fix existing player tests for the new signature**

The old tests call `player.Play(data)` (one arg). Update every call in `internal/audio/player_test.go` to the two-arg form, but pass format **`"wav"`** (NOT `"mp3"`): `player.Play(data, "wav")`. Reason: these tests pass fake bytes that are not valid audio. On Windows, `"wav"` routes to `SoundPlayer`, which **fails fast** on invalid data; `"mp3"` routes to the WPF `MediaPlayer`, which is much slower for unloadable input (it waits up to ~3s for a duration). Using `"wav"` keeps the suite fast and avoids any chance of a hang. The real format-selection coverage is the `buildPlayCommand`/`extForFormat` table test from Step 1 (which does not run playback). Do NOT add new real-playback MP3 tests.

Run: `go test ./internal/audio/ -v`
Expected: PASS (new + updated existing tests).

- [ ] **Step 5: Commit**

```bash
git add internal/audio/player.go internal/audio/player_test.go
git commit -m "feat(audio): format-aware playback; fix Windows MP3 via MediaPlayer"
```

---

## Task 8: Worker pool — inject registry + player, carry format

**Files:**
- Modify: `internal/server/worker.go`
- Modify: `internal/server/worker_test.go`

- [ ] **Step 1: Replace `internal/server/worker_test.go` entirely**

The refactor deletes the `ttsClient`/`audioPlayer` fields, changes `Job.Voice` to a string, and changes `Submit` to take a `SpeakRequest`, so the existing 20+ tests will not compile. Replace the whole file with this focused, equivalent suite. The test doubles (`fakeProvider`, `fakeResolver`, `fakePlayer`, `okResolver`, `waitFor`) live here and are reused by `server_test.go` in Task 9 (same package — do not redefine them there).

```go
// internal/server/worker_test.go
package server

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ybouhjira/claude-code-tts/internal/tts"
)

// --- shared test doubles (also used by server_test.go) ---

type fakeProvider struct{ format string }

func (f *fakeProvider) Name() string         { return "fake" }
func (f *fakeProvider) Voices() []string      { return nil }
func (f *fakeProvider) DefaultFormat() string { return f.format }
func (f *fakeProvider) Synthesize(ctx context.Context, req tts.Request) (tts.Audio, error) {
	return tts.Audio{Data: []byte("AUDIO"), Format: f.format}, nil
}

type fakeResolver struct {
	prov tts.Provider
	err  error
}

func (r fakeResolver) Resolve(profile string) (tts.Provider, tts.Request, error) {
	if r.err != nil {
		return nil, tts.Request{}, r.err
	}
	return r.prov, tts.Request{Voice: "v"}, nil
}
func (r fakeResolver) ResolveVoice(provider, voice string, speed float64) (tts.Provider, tts.Request, error) {
	if r.err != nil {
		return nil, tts.Request{}, r.err
	}
	return r.prov, tts.Request{Voice: voice, Speed: speed}, nil
}
func (r fakeResolver) Default() (tts.Provider, tts.Request, error) { return r.Resolve("default") }

func okResolver(format string) fakeResolver { return fakeResolver{prov: &fakeProvider{format: format}} }

type fakePlayer struct {
	mu         sync.Mutex
	calls      int
	lastData   []byte
	lastFormat string
}

func (p *fakePlayer) Play(data []byte, format string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	p.lastData, p.lastFormat = data, format
	return nil
}
func (p *fakePlayer) IsPlaying() bool { return false }
func (p *fakePlayer) snapshot() (int, []byte, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls, p.lastData, p.lastFormat
}

func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", msg)
}

// --- tests ---

func TestNewWorkerPool(t *testing.T) {
	wp := NewWorkerPool(okResolver("mp3"), &fakePlayer{}, 2, 10)
	if wp.resolver == nil || wp.player == nil {
		t.Fatal("resolver/player not set")
	}
	if wp.workerCount != 2 || wp.queueSize != 10 {
		t.Errorf("got %d/%d", wp.workerCount, wp.queueSize)
	}
}

func TestWorkerPool_ProcessesJobWithFormat(t *testing.T) {
	player := &fakePlayer{}
	wp := NewWorkerPool(okResolver("wav"), player, 1, 4)
	wp.Start()
	defer wp.Stop()

	if _, err := wp.Submit(SpeakRequest{Text: "hello", Profile: "default"}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	waitFor(t, func() bool { n, _, _ := player.snapshot(); return n == 1 }, "player called")
	_, data, format := player.snapshot()
	if string(data) != "AUDIO" || format != "wav" {
		t.Errorf("player got %q/%q, want AUDIO/wav", data, format)
	}
}

func TestWorkerPool_SubmitQueueFull(t *testing.T) {
	// No Start(): nothing drains, so the queue fills at capacity.
	wp := NewWorkerPool(okResolver("mp3"), &fakePlayer{}, 1, 2)
	if _, err := wp.Submit(SpeakRequest{Text: "1", Profile: "default"}); err != nil {
		t.Fatalf("submit 1: %v", err)
	}
	if _, err := wp.Submit(SpeakRequest{Text: "2", Profile: "default"}); err != nil {
		t.Fatalf("submit 2: %v", err)
	}
	if _, err := wp.Submit(SpeakRequest{Text: "3", Profile: "default"}); err == nil {
		t.Fatal("expected queue-full error on third submit")
	}
}

func TestWorkerPool_FailedJobOnResolveError(t *testing.T) {
	wp := NewWorkerPool(fakeResolver{err: errors.New("boom")}, &fakePlayer{}, 1, 4)
	wp.Start()
	defer wp.Stop()
	if _, err := wp.Submit(SpeakRequest{Text: "hi", Profile: "default"}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	waitFor(t, func() bool { return wp.GetStatus().TotalFailed == 1 }, "job failed")
}

func TestWorkerPool_JobHistoryLimit(t *testing.T) {
	wp := NewWorkerPool(okResolver("mp3"), &fakePlayer{}, 1, 200)
	for i := 0; i < 150; i++ {
		_, _ = wp.Submit(SpeakRequest{Text: "t", Profile: "default"})
	}
	if got := len(wp.GetStatus().RecentJobs); got > 10 {
		t.Errorf("recent jobs = %d, want <= 10", got)
	}
}

func TestWorkerPool_StartStop(t *testing.T) {
	wp := NewWorkerPool(okResolver("mp3"), &fakePlayer{}, 2, 10)
	wp.Start()
	wp.Stop() // must not panic or deadlock
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/server/ -run TestWorkerPool_ProcessesJobWithFormat -v`
Expected: FAIL — `NewWorkerPool` signature mismatch / `Submit` arity.

- [ ] **Step 3: Edit `internal/server/worker.go`**

Add interfaces and change the struct, constructor, `Job`, `processJob`, and `Submit`. Replace the indicated pieces:

Add near the top (after imports), and remove the `audio`/`tts` direct construction:

```go
// audioPlayer is the playback dependency (satisfied by *audio.Player).
type audioPlayer interface {
	Play(data []byte, format string) error
	IsPlaying() bool
}

// synthResolver resolves profiles/providers to providers + base requests.
type synthResolver interface {
	Resolve(profile string) (tts.Provider, tts.Request, error)
	ResolveVoice(provider, voice string, speed float64) (tts.Provider, tts.Request, error)
	Default() (tts.Provider, tts.Request, error)
}

// SpeakRequest is the input to Submit. When Provider is set it takes precedence
// over Profile; Voice/Speed override the resolved request.
type SpeakRequest struct {
	Text     string
	Profile  string
	Provider string
	Voice    string
	Speed    float64
}
```

Change `Job` (add fields; `Voice` becomes string):

```go
type Job struct {
	ID        string    `json:"id"`
	Text      string    `json:"text"`
	Profile   string    `json:"profile"`
	Provider  string    `json:"provider"`
	Voice     string    `json:"voice"`
	Speed     float64   `json:"speed"`
	CreatedAt time.Time `json:"created_at"`
	Status    string    `json:"status"`
	Error     string    `json:"error,omitempty"`
	mu        sync.RWMutex
}
```

Change `WorkerPool` fields `ttsClient`/`audioPlayer` to the injected deps:

```go
type WorkerPool struct {
	resolver    synthResolver
	player      audioPlayer
	jobs        chan *Job
	jobHistory  []*Job
	historyMu   sync.RWMutex
	workerCount int
	queueSize   int
	processed   atomic.Int64
	failed      atomic.Int64
	paused      atomic.Bool
	wg          sync.WaitGroup
	shutdown    chan struct{}
}
```

Change the constructor:

```go
// NewWorkerPool creates a pool backed by the given resolver and player.
func NewWorkerPool(resolver synthResolver, player audioPlayer, workerCount, queueSize int) *WorkerPool {
	return &WorkerPool{
		resolver:    resolver,
		player:      player,
		jobs:        make(chan *Job, queueSize),
		jobHistory:  make([]*Job, 0),
		workerCount: workerCount,
		queueSize:   queueSize,
		shutdown:    make(chan struct{}),
	}
}
```

Replace `processJob`:

```go
func (wp *WorkerPool) processJob(job *Job) {
	startTime := time.Now()
	logging.Info("Job %s: starting (profile=%s, voice=%s, text_len=%d)", job.ID, job.Profile, job.Voice, len(job.Text))

	job.mu.Lock()
	job.Status = "processing"
	job.mu.Unlock()

	var provider tts.Provider
	var req tts.Request
	var err error
	if job.Provider != "" {
		provider, req, err = wp.resolver.ResolveVoice(job.Provider, job.Voice, job.Speed)
	} else {
		provider, req, err = wp.resolver.Resolve(job.Profile)
		if err == nil {
			if job.Voice != "" {
				req.Voice = job.Voice
			}
			if job.Speed != 0 {
				req.Speed = job.Speed
			}
		}
	}
	if err != nil {
		wp.failJob(job, fmt.Errorf("resolve: %w", err), startTime)
		return
	}
	req.Text = job.Text

	audioOut, err := provider.Synthesize(context.Background(), req)
	if err != nil {
		wp.failJob(job, fmt.Errorf("synthesis: %w", err), startTime)
		return
	}

	if err := wp.player.Play(audioOut.Data, audioOut.Format); err != nil {
		wp.failJob(job, fmt.Errorf("playback: %w", err), startTime)
		return
	}

	job.mu.Lock()
	job.Status = "completed"
	job.mu.Unlock()
	wp.processed.Add(1)
	logging.Info("Job %s: completed in %v", job.ID, time.Since(startTime))
}

func (wp *WorkerPool) failJob(job *Job, err error, start time.Time) {
	job.mu.Lock()
	job.Status = "failed"
	job.Error = err.Error()
	job.mu.Unlock()
	wp.failed.Add(1)
	logging.Error("Job %s: %v (after %v)", job.ID, err, time.Since(start))
}
```

Replace `Submit` signature/body head:

```go
// Submit adds a new job to the queue.
func (wp *WorkerPool) Submit(sr SpeakRequest) (*Job, error) {
	job := &Job{
		ID:        fmt.Sprintf("job-%d", time.Now().UnixNano()),
		Text:      sr.Text,
		Profile:   sr.Profile,
		Provider:  sr.Provider,
		Voice:     sr.Voice,
		Speed:     sr.Speed,
		CreatedAt: time.Now(),
		Status:    "pending",
	}

	wp.historyMu.Lock()
	wp.jobHistory = append(wp.jobHistory, job)
	if len(wp.jobHistory) > 100 {
		wp.jobHistory = wp.jobHistory[1:]
	}
	wp.historyMu.Unlock()

	select {
	case wp.jobs <- job:
		return job, nil
	default:
		job.Status = "failed"
		job.Error = "queue is full"
		return job, fmt.Errorf("job queue is full (size: %d)", wp.queueSize)
	}
}
```

Update imports: add `"context"` and `"github.com/ybouhjira/claude-code-tts/internal/tts"`; remove `"github.com/ybouhjira/claude-code-tts/internal/audio"`. In `GetStatus`, the per-job copy literal now uses the string `Voice` and includes the new `Provider` field — copy `ID, Text, Profile, Provider, Voice, Speed, CreatedAt, Status, Error` — and the player call `wp.audioPlayer.IsPlaying()` becomes `wp.player.IsPlaying()`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/server/ -run 'TestWorkerPool|TestNewWorkerPool' -v`
Expected: PASS. (`worker_test.go` was replaced wholesale in Step 1, so there are no stale references to the removed `ttsClient`/`audioPlayer` fields, the `[]tts.Voice` table, or `Submit(text, voice)`.)

- [ ] **Step 5: Commit**

```bash
git add internal/server/worker.go internal/server/worker_test.go
git commit -m "feat(server): inject resolver+player into worker pool; carry audio format"
```

---

## Task 9: MCP server wiring + speak args

**Files:**
- Modify: `internal/server/server.go`
- Modify: `internal/server/server_test.go`

- [ ] **Step 1: Replace `internal/server/server_test.go` entirely**

The existing file asserts old behavior (invalid-voice rejection; voice names like "nova"/"alloy" in the result text) and already defines `TestHandleSpeak_MissingText` and `TestNew`, so it must be **replaced**, not appended to. The new file reuses the test doubles defined in `worker_test.go` (Task 8 — `okResolver`, `fakePlayer`).

```go
// internal/server/server_test.go
package server

import (
	"context"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	wp := NewWorkerPool(okResolver("mp3"), &fakePlayer{}, 1, 8)
	wp.Start()
	t.Cleanup(wp.Stop)
	return &Server{workerPool: wp}
}

func speakReq(args map[string]any) mcp.CallToolRequest {
	var r mcp.CallToolRequest
	r.Params.Arguments = args
	return r
}

func TestNew(t *testing.T) {
	s, err := New() // back-compat default path must succeed with NO API key set
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Shutdown()
	if s.mcpServer == nil || s.workerPool == nil {
		t.Fatal("server not fully constructed")
	}
}

func TestHandleSpeak_QueuesWithProfile(t *testing.T) {
	s := newTestServer(t)
	res, err := s.handleSpeak(context.Background(), speakReq(map[string]any{
		"text": "hello", "profile": "default", "speed": 1.2,
	}))
	if err != nil {
		t.Fatalf("handleSpeak: %v", err)
	}
	if res.IsError {
		t.Fatal("unexpected error result")
	}
	text := res.Content[0].(mcp.TextContent).Text
	if !strings.Contains(text, "job-") || !strings.Contains(text, "default") {
		t.Errorf("result = %q", text)
	}
}

func TestHandleSpeak_QueuesWithProvider(t *testing.T) {
	s := newTestServer(t)
	res, err := s.handleSpeak(context.Background(), speakReq(map[string]any{
		"text": "hi", "provider": "grok", "voice": "leo",
	}))
	if err != nil || res.IsError {
		t.Fatalf("handleSpeak err=%v", err)
	}
	if !strings.Contains(res.Content[0].(mcp.TextContent).Text, "provider:grok") {
		t.Errorf("result = %q", res.Content[0].(mcp.TextContent).Text)
	}
}

func TestHandleSpeak_MissingText(t *testing.T) {
	s := newTestServer(t)
	res, _ := s.handleSpeak(context.Background(), speakReq(map[string]any{}))
	if !res.IsError {
		t.Error("expected error for missing text")
	}
}

func TestHandleSpeak_EmptyText(t *testing.T) {
	s := newTestServer(t)
	res, _ := s.handleSpeak(context.Background(), speakReq(map[string]any{"text": ""}))
	if !res.IsError {
		t.Error("expected error for empty text")
	}
}

func TestHandleSpeak_TextTooLong(t *testing.T) {
	s := newTestServer(t)
	res, _ := s.handleSpeak(context.Background(), speakReq(map[string]any{"text": strings.Repeat("a", 4097)}))
	if !res.IsError {
		t.Error("expected error for too-long text")
	}
}

func TestHandleStatus(t *testing.T) {
	s := newTestServer(t)
	res, err := s.handleStatus(context.Background(), speakReq(nil))
	if err != nil || res.IsError {
		t.Fatalf("handleStatus failed: %v", err)
	}
	if !strings.Contains(res.Content[0].(mcp.TextContent).Text, "worker_count") {
		t.Error("status missing worker_count")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/server/ -run TestHandleSpeak -v`
Expected: FAIL — `handleSpeak` still uses old `Submit`/voice validation.

- [ ] **Step 3: Edit `internal/server/server.go`**

Change `New()` to build the registry + player and inject them:

```go
func New() (*Server, error) {
	logging.Info("Creating TTS MCP server...")

	reg := ttsconfig.LoadOrDefault()
	player := audio.NewPlayer()
	wp := NewWorkerPool(reg, player, 2, 50)
	wp.Start()
	logging.Info("Worker pool created and started")

	mcpSrv := server.NewMCPServer("claude-code-tts", "1.0.0", server.WithToolCapabilities(true))
	s := &Server{mcpServer: mcpSrv, workerPool: wp}
	s.registerTools()
	return s, nil
}
```

Add imports: `"github.com/ybouhjira/claude-code-tts/internal/audio"` and `"github.com/ybouhjira/claude-code-tts/internal/ttsconfig"`; remove the now-unused `"github.com/ybouhjira/claude-code-tts/internal/tts"` import if nothing else uses it.

Update the `speak` tool registration to add `profile`/`speed` and adjust the `voice` description:

```go
	speakTool := mcp.NewTool("speak",
		mcp.WithDescription("Convert text to speech and play it aloud. Use this to provide audio feedback to the user."),
		mcp.WithString("text", mcp.Required(),
			mcp.Description("The text to convert to speech (max 4096 characters)")),
		mcp.WithString("profile",
			mcp.Description("Named voice profile from config (e.g. default, error). Defaults to the configured default profile.")),
		mcp.WithString("provider",
			mcp.Description("Use an explicit provider (openai, grok, piper) instead of a profile.")),
		mcp.WithString("voice",
			mcp.Description("Override the profile's voice (provider-specific id, e.g. alloy/onyx for OpenAI or eve/leo for Grok).")),
		mcp.WithNumber("speed",
			mcp.Description("Override speech speed (provider-dependent range, e.g. 0.7-1.5).")),
	)
```

Replace `handleSpeak` with profile/voice/speed extraction (drop the OpenAI-only `IsValidVoice` check — validation now happens in the resolver):

```go
func (s *Server) handleSpeak(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	text, ok := request.Params.Arguments["text"].(string)
	if !ok || text == "" {
		return mcp.NewToolResultError("text parameter is required"), nil
	}
	if len(text) > 4096 {
		return mcp.NewToolResultError("text exceeds maximum length of 4096 characters"), nil
	}

	str := func(k string) string {
		if v, ok := request.Params.Arguments[k].(string); ok {
			return v
		}
		return ""
	}
	profile, provider, voice := str("profile"), str("provider"), str("voice")
	var speed float64
	if v, ok := request.Params.Arguments["speed"].(float64); ok {
		speed = v
	}
	if profile == "" && provider == "" {
		profile = "default"
	}

	job, err := s.workerPool.Submit(SpeakRequest{
		Text: text, Profile: profile, Provider: provider, Voice: voice, Speed: speed,
	})
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to queue TTS job: %v", err)), nil
	}
	sel := profile
	if provider != "" {
		sel = "provider:" + provider
	}
	return mcp.NewToolResultText(fmt.Sprintf("TTS job queued (ID: %s, %s)", job.ID, sel)), nil
}
```

> **Default-profile fallback:** `handleSpeak` defaults the profile to `"default"` (only when neither `profile` nor `provider` is given). If a user's config lacks a `default` profile, `Resolve` errors at job time and the job is marked failed (visible via `tts_status`). This matches the spec's graceful-degradation intent.

Also edit **`cmd/tts-server/main.go`**: remove the hard `OPENAI_API_KEY` requirement so a Grok-only or Piper-only config can start. Delete the block at lines 28–32:

```go
	// Check for required environment variable
	if os.Getenv("OPENAI_API_KEY") == "" {
		logging.Fatal("OPENAI_API_KEY environment variable is required")
	}
	logging.Info("OPENAI_API_KEY is set (length: %d)", len(os.Getenv("OPENAI_API_KEY")))
```

and replace it with a non-fatal note (keys are validated per-provider at resolve time by the registry):

```go
	// API keys are validated per-provider at resolve time (see internal/ttsconfig).
	// Do not hard-require OPENAI_API_KEY here — Grok/Piper configs need no OpenAI key.
```

The `os` import is still used elsewhere in `main.go` (e.g. `os.Getpid`, `os.Exit`), so leave it.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/server/ -v`
Expected: PASS (worker + server tests).

- [ ] **Step 5: Commit**

```bash
git add internal/server/server.go internal/server/server_test.go cmd/tts-server/main.go
git commit -m "feat(server): load registry; speak accepts profile/provider/voice/speed; drop hard OPENAI_API_KEY require"
```

---

## Task 10: speak-text CLI

**Files:**
- Modify: `cmd/speak-text/main.go`

- [ ] **Step 1: Replace `cmd/speak-text/main.go`**

(No unit test — this is a thin `main`. It compiles and is exercised manually. The logic it calls is covered by `tts`/`ttsconfig` tests.)

```go
// cmd/speak-text/main.go
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/ybouhjira/claude-code-tts/internal/audio"
	"github.com/ybouhjira/claude-code-tts/internal/tts"
	"github.com/ybouhjira/claude-code-tts/internal/ttsconfig"
)

func main() {
	providerFlag := flag.String("provider", "", "Use an explicit provider (openai, grok, piper) instead of a profile")
	profile := flag.String("profile", "default", "Voice profile from config")
	voice := flag.String("voice", "", "Override the profile's voice")
	speed := flag.Float64("speed", 0, "Override speech speed (0 = profile default)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [OPTIONS] TEXT\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Converts text to speech via the configured provider and plays it.\n\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nExamples:\n")
		fmt.Fprintf(os.Stderr, "  %s \"Build completed\"\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s -profile error \"Build failed\"\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s -provider grok -voice leo \"Build failed\"\n", os.Args[0])
	}
	flag.Parse()

	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(1)
	}
	text := flag.Arg(0)

	reg := ttsconfig.LoadOrDefault()

	var prov tts.Provider
	var req tts.Request
	var err error
	if *providerFlag != "" {
		prov, req, err = reg.ResolveVoice(*providerFlag, *voice, *speed)
	} else {
		prov, req, err = reg.Resolve(*profile)
		if err == nil {
			if *voice != "" {
				req.Voice = *voice
			}
			if *speed != 0 {
				req.Speed = *speed
			}
		}
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	req.Text = text

	out, err := prov.Synthesize(context.Background(), req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error synthesizing speech: %v\n", err)
		os.Exit(1)
	}

	if err := audio.NewPlayer().Play(out.Data, out.Format); err != nil {
		fmt.Fprintf(os.Stderr, "Error playing audio: %v\n", err)
		os.Exit(1)
	}
}
```

- [ ] **Step 2: Verify it builds**

Run: `go build ./cmd/speak-text/`
Expected: builds with no errors.

- [ ] **Step 3: Commit**

```bash
git add cmd/speak-text/main.go
git commit -m "feat(cli): speak-text uses provider registry with -profile/-voice/-speed"
```

---

## Task 11: Relay — ctx-aware MP3 synthesizer + fail-fast on WAV provider

**Files:**
- Modify: `internal/relay/synthesizer.go`
- Create (in same file or new): `internal/relay/provider_synth.go` + `provider_synth_test.go`
- Modify: `internal/relay/handler.go`
- Modify: `internal/relay/handler_test.go` (and any other relay test using the mock synthesizer)
- Modify: `cmd/relay/main.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/relay/provider_synth_test.go
package relay

import (
	"context"
	"testing"

	"github.com/ybouhjira/claude-code-tts/internal/tts"
)

type stubProvider struct{ format string }

func (s stubProvider) Name() string          { return "stub" }
func (s stubProvider) Voices() []string       { return nil }
func (s stubProvider) DefaultFormat() string  { return s.format }
func (s stubProvider) Synthesize(ctx context.Context, r tts.Request) (tts.Audio, error) {
	return tts.Audio{Data: []byte("MP3"), Format: s.format}, nil
}

func TestProviderSynthesizer_RejectsWAV(t *testing.T) {
	if _, err := NewProviderSynthesizer(stubProvider{format: "wav"}, tts.Request{}); err == nil {
		t.Fatal("expected error for WAV provider on relay path")
	}
}

func TestProviderSynthesizer_MP3(t *testing.T) {
	s, err := NewProviderSynthesizer(stubProvider{format: "mp3"}, tts.Request{Voice: "eve"})
	if err != nil {
		t.Fatalf("NewProviderSynthesizer: %v", err)
	}
	data, err := s.Synthesize(context.Background(), "hello")
	if err != nil || string(data) != "MP3" {
		t.Errorf("got %q err %v", data, err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/relay/ -run TestProviderSynthesizer -v`
Expected: FAIL — `undefined: NewProviderSynthesizer`.

- [ ] **Step 3: Implement and rewire**

Replace `internal/relay/synthesizer.go`:

```go
// internal/relay/synthesizer.go
package relay

import "context"

// Synthesizer converts text to MP3 bytes for the relay/companion path.
type Synthesizer interface {
	Synthesize(ctx context.Context, text string) ([]byte, error)
}
```

Create `internal/relay/provider_synth.go`:

```go
// internal/relay/provider_synth.go
package relay

import (
	"context"
	"fmt"

	"github.com/ybouhjira/claude-code-tts/internal/tts"
)

// ProviderSynthesizer adapts a tts.Provider to the relay Synthesizer, enforcing
// MP3 output (the companion/clip store require MP3).
type ProviderSynthesizer struct {
	provider tts.Provider
	base     tts.Request
}

// NewProviderSynthesizer fails fast when the provider does not emit MP3.
func NewProviderSynthesizer(p tts.Provider, base tts.Request) (*ProviderSynthesizer, error) {
	if p.DefaultFormat() != "mp3" {
		return nil, fmt.Errorf("relay requires an MP3 provider; %q emits %s — use OpenAI or Grok for the relay", p.Name(), p.DefaultFormat())
	}
	return &ProviderSynthesizer{provider: p, base: base}, nil
}

func (s *ProviderSynthesizer) Synthesize(ctx context.Context, text string) ([]byte, error) {
	req := s.base
	req.Text = text
	out, err := s.provider.Synthesize(ctx, req)
	if err != nil {
		return nil, err
	}
	return out.Data, nil
}
```

Edit `internal/relay/handler.go` — `handleIngest` now passes ctx and no voice. Change the synthesis call (was `h.synth.Synthesize(req.Text, tts.VoiceAlloy)`):

```go
	audioBytes, err := h.synth.Synthesize(r.Context(), req.Text)
```

Remove the now-unused `tts` import from `handler.go` if `tts.VoiceAlloy` was its only use.

Edit `cmd/relay/main.go` — replace the `synth := tts.NewClient()` line (currently line ~68) and wiring with registry-based resolution + fail-fast:

```go
	reg := ttsconfig.LoadOrDefault()
	provider, baseReq, err := reg.Default()
	if err != nil {
		logging.Fatal("failed to resolve TTS profile: %v", err)
	}
	synth, err := relay.NewProviderSynthesizer(provider, baseReq)
	if err != nil {
		logging.Fatal("%v", err) // WAV provider configured for relay -> fail fast
	}
```

Add the `ttsconfig` import to `cmd/relay/main.go`; keep the `tts` import only if still used elsewhere (the `relay.NewServer(ingestAddr, synth, store, hub)` call now receives `synth` of type `*relay.ProviderSynthesizer`, which satisfies `relay.Synthesizer`). Remove the OPENAI_API_KEY hard-require block at main.go:30-32 **only if** you want non-OpenAI providers to start without it; otherwise leave it (it still applies to the default OpenAI config). Recommended: replace that block with a comment noting the key requirement is now per-provider and enforced at resolve time.

- [ ] **Step 4: Update the two relay synthesizer test doubles**

There are TWO test doubles implementing `Synthesizer`, in separate files; both must change from `Synthesize(text string, voice tts.Voice)` to `Synthesize(ctx context.Context, text string)`:

1. **`internal/relay/handler_test.go`** — `mockSynthesizer` (struct ~line 21, method ~line 28). Change the method to:

```go
func (m *mockSynthesizer) Synthesize(_ context.Context, text string) ([]byte, error) {
	m.called = true
	m.calledWith = text
	return m.audioData, m.err
}
```

(preserve its existing fields — `audioData`, `err`, `called`, `calledWith`). Add `"context"` to the imports and **remove** the now-unused `"github.com/ybouhjira/claude-code-tts/internal/tts"` import (it was used only for `tts.Voice`). `push_suppression_test.go` only constructs `&mockSynthesizer{...}`, so it needs no change.

2. **`internal/relay/presence_gaps_test.go`** — `concurrentSafeSynth` (method ~line 39). Change to:

```go
func (s *concurrentSafeSynth) Synthesize(_ context.Context, _ string) ([]byte, error) {
	return s.data, nil
}
```

Ensure `"context"` is imported and **remove** the now-unused `"github.com/ybouhjira/claude-code-tts/internal/tts"` import.

Run: `go test ./internal/relay/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/relay/ cmd/relay/main.go
git commit -m "feat(relay): ctx-aware MP3 synthesizer via registry; fail fast on WAV provider"
```

---

## Task 12: Example config, docs, and full build/test

**Files:**
- Create: `config.example.json`
- Modify: `README.md`, `CLAUDE.md`

- [ ] **Step 1: Create `config.example.json`**

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

- [ ] **Step 2: Document in `README.md` and `CLAUDE.md`**

Add a "Providers & Configuration" section to `README.md`: the three providers, the `config.json` location (`~/.claude/plugins/claude-code-tts/config.json`, overridable via `CLAUDE_TTS_CONFIG`), named profiles, the `CLAUDE_TTS_PROFILE/PROVIDER/VOICE/SPEED/MODEL` env overrides, that **API keys come only from env** (`OPENAI_API_KEY`, `XAI_API_KEY`), and that **Piper is local-playback only** (the relay/companion path requires OpenAI or Grok). Update `CLAUDE.md`'s `internal/tts` description to mention `Provider`/`ttsconfig`.

- [ ] **Step 3: Format, build, vet, and test**

Run:
```bash
gofmt -w .
go build ./...
go vet ./...
go test ./...
```
Expected: all packages build and pass. `gofmt -w .` normalizes the manually-aligned code blocks pasted from this plan. The **`Makefile` needs no change** — its `build`/`test`/`lint` targets operate over `./...`, which already picks up the new `internal/tts`, `internal/ttsconfig`, and `cmd/*` code (this closes spec §11's Makefile line item). Fix any remaining call sites surfaced by the compiler (most likely leftover `tts.NewClient()`, `Play(data)`, or `Submit(text, voice)` references).

- [ ] **Step 4: Commit**

```bash
git add config.example.json README.md CLAUDE.md
git commit -m "docs(tts): document providers, config file, and profiles"
```

---

## Self-Review Notes (for the implementer)

- **Spec coverage:** Providers (Tasks 2–4); config + profiles (Tasks 5–6); registry API `Resolve`/`ResolveVoice`/`Default`/`Load`/`LoadOrDefault` and the full `CLAUDE_TTS_PROFILE/PROVIDER/VOICE/SPEED/MODEL` override set (Task 6); format-aware playback incl. Windows MP3 fix (Task 7); consumer wiring — worker/MCP/CLI/relay (Tasks 8–11) with the `provider` selector on both the `speak` tool (Task 9) and the CLI (Task 10); the `tts-server` entrypoint no longer hard-requires `OPENAI_API_KEY` (Task 9); backward-compat default (Task 5 `DefaultConfig`, Task 6 `LoadOrDefault`); Piper-local-only + relay fail-fast (Task 11); real-method test coverage (Tasks 2–3); config.example + docs + Makefile note (Task 12).
- **Type consistency:** `Request{Text,Voice,Speed,Model}`, `Audio{Data,Format}`, and `Provider` (`Name`/`Voices`/`DefaultFormat`/`Synthesize`) are identical across all tasks. The worker depends on `synthResolver` (`Resolve`/`ResolveVoice`/`Default`) + `audioPlayer`, satisfied by `*ttsconfig.Registry` and `*audio.Player`. `Submit(SpeakRequest)` and the `SpeakRequest{Text,Profile,Provider,Voice,Speed}` struct are used identically in Task 8 (impl + tests) and Task 9 (`handleSpeak`). Relay uses `Synthesizer` (`ctx, text → []byte`).
- **Naming caveat:** Task 6 renames `ttsconfig.Load()` (Config) from Task 5 to `loadConfig()` and introduces `Load() (*Registry, error)` + `LoadOrDefault()`. Apply that rename when doing Task 6 so the package has one public `Load`. `Resolve`/`ResolveVoice`/`Default` are then consumed by the worker (Task 8/9) and CLI (Task 10).
- **Out of scope (later sub-projects):** Telegram delivery, caching/cost controls, smarter "what to speak", WAV→MP3 transcoding. The known `WorkerPool.Stop()` send-on-closed-channel race from the review is intentionally not addressed here.
