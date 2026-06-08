# Voice Output Mode + Telegram Delivery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a persistent voice output mode (`off`/`computer`/`phone`/`both`) — settable from chat (a new `tts_output` MCP tool) and the CLI — plus Telegram delivery of synthesized audio via the Bot API, routed by the worker.

**Architecture:** A tiny `voicemode` package persists the mode to a JSON state file. A `telegram` package POSTs audio to `api.telegram.org/sendAudio`. The MCP worker reads the mode and routes each synthesized clip to the local player and/or Telegram. `off` short-circuits before synthesis. The relay/PWA subsystem is untouched.

**Tech Stack:** Go 1.23 (per `go.mod`; local toolchain may be newer), stdlib `net/http` + `mime/multipart`, the existing `tts`/`ttsconfig`/`audio` packages and `mcp-go`. No new Go modules, no `go.mod` bump (all new code is 1.23-compatible).

**Spec:** `docs/superpowers/specs/2026-06-08-voice-mode-and-telegram-design.md`
**Module path:** `github.com/ybouhjira/claude-code-tts`

**Convention:** When pasting a code block that starts with a `// path/to/file.go` comment, OMIT that first line — the repo does not use file-path header comments. Start files at `package <name>`. Run `gofmt -w` on changed files before committing.

---

## File Structure

**New**
- `internal/voicemode/voicemode.go` (+ `_test.go`) — `Mode` type, `Store` (load/save the state file), helpers.
- `internal/telegram/telegram.go` (+ `_test.go`) — `Sender.SendAudio`.

**Modified**
- `internal/ttsconfig/config.go` — `TelegramConfig` + `Config.Telegram`.
- `internal/ttsconfig/registry.go` — `(*Registry).TelegramSender()` builder.
- `internal/server/worker.go` (+ `worker_test.go`) — mode-aware `processJob`; `WithMode`/`WithTelegram`; `PoolStatus.VoiceMode`.
- `internal/server/server.go` (+ `server_test.go`) — wire mode store + telegram; `tts_output` tool.
- `cmd/speak-text/main.go` — `mode`/`status` subcommands + `-to` flag.
- `config.example.json`, `README.md`, `CLAUDE.md` — docs.

**Coupling note:** Task 5 changes both `worker.go` and `server.go` (same `server` package) — they must be done together or the package won't compile. All other tasks are independent packages.

---

## Task 1: `voicemode` package

**Files:**
- Create: `internal/voicemode/voicemode.go`
- Test: `internal/voicemode/voicemode_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/voicemode/voicemode_test.go
package voicemode

import (
	"path/filepath"
	"testing"
)

func TestValidAndHelpers(t *testing.T) {
	for _, m := range []Mode{Off, Computer, Phone, Both} {
		if !Valid(m) {
			t.Errorf("%q should be valid", m)
		}
	}
	if Valid("loud") {
		t.Error("\"loud\" should be invalid")
	}
	cases := []struct {
		m                  Mode
		local, telegram    bool
	}{
		{Off, false, false},
		{Computer, true, false},
		{Phone, false, true},
		{Both, true, true},
	}
	for _, c := range cases {
		if c.m.PlaysLocal() != c.local || c.m.SendsTelegram() != c.telegram {
			t.Errorf("%q: local=%v telegram=%v, want %v/%v", c.m, c.m.PlaysLocal(), c.m.SendsTelegram(), c.local, c.telegram)
		}
	}
}

func TestStore_DefaultWhenMissing(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "nope.json"))
	if got := s.Get(); got != Computer {
		t.Errorf("missing file -> %q, want computer", got)
	}
}

func TestStore_SetGetRoundTrip(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "state.json"))
	if err := s.Set(Phone); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got := s.Get(); got != Phone {
		t.Errorf("got %q, want phone", got)
	}
}

func TestStore_SetInvalid(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "state.json"))
	if err := s.Set("sideways"); err == nil {
		t.Error("expected error for invalid mode")
	}
}

func TestStore_GetInvalidContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := writeFileHelper(path, `{"mode":"wat"}`); err != nil {
		t.Fatal(err)
	}
	s := NewStore(path)
	if got := s.Get(); got != Computer {
		t.Errorf("invalid content -> %q, want computer (default)", got)
	}
}
```

- [ ] **Step 2: Add the test helper and run to verify it fails**

Append to the test file:

```go
import "os"

func writeFileHelper(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}
```

(Put the `os` import in the existing import block rather than a second `import` line.)

Run: `go test ./internal/voicemode/ -v`
Expected: FAIL — package/`Mode`/`NewStore` undefined.

- [ ] **Step 3: Write the implementation**

```go
// internal/voicemode/voicemode.go
package voicemode

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Mode is the voice output destination.
type Mode string

const (
	Off      Mode = "off"
	Computer Mode = "computer"
	Phone    Mode = "phone"
	Both     Mode = "both"
)

// Valid reports whether m is one of the four known modes.
func Valid(m Mode) bool {
	switch m {
	case Off, Computer, Phone, Both:
		return true
	}
	return false
}

// PlaysLocal reports whether this mode plays on the local speakers.
func (m Mode) PlaysLocal() bool { return m == Computer || m == Both }

// SendsTelegram reports whether this mode sends to Telegram.
func (m Mode) SendsTelegram() bool { return m == Phone || m == Both }

// Store persists the current Mode to a JSON file.
type Store struct {
	path string
	mu   sync.Mutex
}

// NewStore creates a Store backed by the file at path.
func NewStore(path string) *Store { return &Store{path: path} }

// DefaultStore uses ~/.claude/plugins/claude-code-tts/state.json
// (overridable via CLAUDE_TTS_STATE).
func DefaultStore() *Store { return NewStore(statePath()) }

func statePath() string {
	if p := os.Getenv("CLAUDE_TTS_STATE"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "plugins", "claude-code-tts", "state.json")
}

type state struct {
	Mode Mode `json:"mode"`
}

// Get returns the persisted mode, defaulting to Computer when the file is
// missing, unreadable, or holds an invalid value.
func (s *Store) Get() Mode {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if err != nil {
		return Computer
	}
	var st state
	if err := json.Unmarshal(data, &st); err != nil || !Valid(st.Mode) {
		return Computer
	}
	return st.Mode
}

// Set validates m and atomically writes it to the state file.
func (s *Store) Set(m Mode) error {
	if !Valid(m) {
		return fmt.Errorf("voicemode: invalid mode %q (valid: off, computer, phone, both)", m)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if dir := filepath.Dir(s.path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("voicemode: create dir: %w", err)
		}
	}
	data, _ := json.Marshal(state{Mode: m})
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("voicemode: write: %w", err)
	}
	return os.Rename(tmp, s.path)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/voicemode/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/voicemode/
git commit -m "feat(voicemode): persistent voice output mode store"
```

---

## Task 2: `telegram` package

**Files:**
- Create: `internal/telegram/telegram.go`
- Test: `internal/telegram/telegram_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/telegram/telegram_test.go
package telegram

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSendAudio(t *testing.T) {
	var gotPath, gotChat string
	var gotAudio []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("ParseMultipartForm: %v", err)
		}
		gotChat = r.FormValue("chat_id")
		f, _, err := r.FormFile("audio")
		if err != nil {
			t.Errorf("FormFile audio: %v", err)
		} else {
			buf := make([]byte, 64)
			n, _ := f.Read(buf)
			gotAudio = buf[:n]
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	s := NewSender("TOKEN123", "555")
	s.baseURL = srv.URL
	if err := s.SendAudio(context.Background(), []byte("MP3DATA"), "hello"); err != nil {
		t.Fatalf("SendAudio: %v", err)
	}
	if gotPath != "/botTOKEN123/sendAudio" {
		t.Errorf("path = %q, want /botTOKEN123/sendAudio", gotPath)
	}
	if gotChat != "555" {
		t.Errorf("chat_id = %q, want 555", gotChat)
	}
	if string(gotAudio) != "MP3DATA" {
		t.Errorf("audio = %q, want MP3DATA", gotAudio)
	}
}

func TestSendAudio_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false,"description":"chat not found"}`))
	}))
	defer srv.Close()

	s := NewSender("T", "0")
	s.baseURL = srv.URL
	err := s.SendAudio(context.Background(), []byte("x"), "")
	if err == nil || !strings.Contains(err.Error(), "chat not found") {
		t.Fatalf("expected error containing API description, got %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/telegram/ -v`
Expected: FAIL — `NewSender` undefined.

- [ ] **Step 3: Write the implementation**

```go
// internal/telegram/telegram.go
package telegram

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"time"
)

// Sender delivers audio to a Telegram chat via the Bot API.
type Sender struct {
	token      string
	chatID     string
	baseURL    string
	httpClient *http.Client
}

// NewSender creates a Sender for the given bot token and chat id.
func NewSender(token, chatID string) *Sender {
	return &Sender{
		token:      token,
		chatID:     chatID,
		baseURL:    "https://api.telegram.org",
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// SendAudio uploads audio (MP3) to the chat as a tappable audio message.
// caption is optional (e.g. the spoken text).
func (s *Sender) SendAudio(ctx context.Context, audio []byte, caption string) error {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	_ = w.WriteField("chat_id", s.chatID)
	if caption != "" {
		_ = w.WriteField("caption", caption)
	}
	part, err := w.CreateFormFile("audio", "clip.mp3")
	if err != nil {
		return fmt.Errorf("telegram: create form file: %w", err)
	}
	if _, err := part.Write(audio); err != nil {
		return fmt.Errorf("telegram: write audio: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("telegram: close writer: %w", err)
	}

	url := fmt.Sprintf("%s/bot%s/sendAudio", s.baseURL, s.token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &body)
	if err != nil {
		return fmt.Errorf("telegram: create request: %w", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("telegram: request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("telegram: API error (status %d): %s", resp.StatusCode, string(b))
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/telegram/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/telegram/
git commit -m "feat(telegram): Bot API sendAudio sender"
```

---

## Task 3: Telegram config + registry builder

**Files:**
- Modify: `internal/ttsconfig/config.go`
- Modify: `internal/ttsconfig/registry.go`
- Test: `internal/ttsconfig/registry_test.go` (add)

- [ ] **Step 1: Write the failing test**

Add to `internal/ttsconfig/registry_test.go` (a fresh `cfg` per sub-case — `testConfig()` already exists there from the foundation work; `Registry` holds `cfg` by pointer, so do NOT mutate a shared one across cases):

```go
func TestRegistry_TelegramSender(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "tok")

	// Configured -> sender.
	cfg := testConfig()
	cfg.Telegram = &TelegramConfig{BotTokenEnv: "TELEGRAM_BOT_TOKEN", ChatID: "42"}
	if s, reason := mustRegistry(t, cfg).TelegramSender(); s == nil {
		t.Fatalf("expected a sender, got nil (reason=%q)", reason)
	}

	// Missing chat id -> nil + reason.
	cfg2 := testConfig()
	cfg2.Telegram = &TelegramConfig{BotTokenEnv: "TELEGRAM_BOT_TOKEN", ChatID: ""}
	if s2, r2 := mustRegistry(t, cfg2).TelegramSender(); s2 != nil || r2 == "" {
		t.Errorf("expected nil sender + reason for missing chat id, got %v/%q", s2, r2)
	}

	// No telegram section -> nil + reason.
	if s3, r3 := mustRegistry(t, testConfig()).TelegramSender(); s3 != nil || r3 == "" {
		t.Errorf("expected nil sender + reason when unconfigured, got %v/%q", s3, r3)
	}
}

func mustRegistry(t *testing.T, cfg *Config) *Registry {
	t.Helper()
	r, err := NewRegistry(cfg)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	return r
}
```

Also add a JSON-parsing test to `internal/ttsconfig/config_test.go` (it already imports `os` and `path/filepath`; `loadConfig` is package-private and reachable from this same-package test) — this satisfies the spec §9 "telegram section parsing" item:

```go
func TestLoadConfig_TelegramSection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{
      "default_provider":"openai",
      "providers":{"openai":{"api_key_env":"OPENAI_API_KEY"}},
      "profiles":{"default":{"provider":"openai","voice":"alloy"}},
      "telegram":{"bot_token_env":"TELEGRAM_BOT_TOKEN","chat_id":"99"}
    }`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_TTS_CONFIG", path)
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Telegram == nil || cfg.Telegram.ChatID != "99" || cfg.Telegram.BotTokenEnv != "TELEGRAM_BOT_TOKEN" {
		t.Errorf("telegram parse wrong: %+v", cfg.Telegram)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/ttsconfig/ -run TestRegistry_TelegramSender -v`
Expected: FAIL — `TelegramConfig` / `TelegramSender` undefined.

- [ ] **Step 3a: Add `TelegramConfig` to `config.go`**

In `internal/ttsconfig/config.go`, add the struct (next to `ProviderConfig`/`Profile`):

```go
// TelegramConfig configures Telegram delivery. The bot token is read from the
// named environment variable; the chat id is stored directly (not secret).
type TelegramConfig struct {
	BotTokenEnv string `json:"bot_token_env"`
	ChatID      string `json:"chat_id"`
}
```

And add a field to `Config`:

```go
type Config struct {
	DefaultProvider string                    `json:"default_provider"`
	DefaultProfile  string                    `json:"default_profile"`
	Providers       map[string]ProviderConfig `json:"providers"`
	Profiles        map[string]Profile        `json:"profiles"`
	Telegram        *TelegramConfig           `json:"telegram,omitempty"`
}
```

- [ ] **Step 3b: Add the builder to `registry.go`**

In `internal/ttsconfig/registry.go`, add the import `"github.com/ybouhjira/claude-code-tts/internal/telegram"` and this method:

```go
// TelegramSender builds a Telegram sender from config + env, or returns
// (nil, reason) describing why it is unavailable.
func (r *Registry) TelegramSender() (*telegram.Sender, string) {
	tc := r.cfg.Telegram
	if tc == nil {
		return nil, "no \"telegram\" section in config.json"
	}
	env := nameOr(tc.BotTokenEnv, "TELEGRAM_BOT_TOKEN")
	token := os.Getenv(env)
	if token == "" {
		return nil, fmt.Sprintf("set $%s", env)
	}
	if tc.ChatID == "" {
		return nil, "set telegram.chat_id in config.json"
	}
	return telegram.NewSender(token, tc.ChatID), ""
}
```

(`nameOr`, `os`, and `fmt` already exist in `registry.go`.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/ttsconfig/ -v`
Expected: PASS (telegram + existing tests).

- [ ] **Step 5: Commit**

```bash
git add internal/ttsconfig/
git commit -m "feat(ttsconfig): telegram config + registry sender builder"
```

---

## Task 4: Mode-aware worker routing (server package — worker.go + server.go together)

This task touches both `worker.go` and `server.go`; do them together so the package compiles. The worker gains mode + telegram via nil-safe builder methods (existing `NewWorkerPool` callers are unchanged — mode `nil` defaults to Computer).

**Files:**
- Modify: `internal/server/worker.go`
- Modify: `internal/server/worker_test.go`
- Modify: `internal/server/server.go`
- Modify: `internal/server/server_test.go`

- [ ] **Step 1: Add worker tests (and the shared doubles they need)**

In `internal/server/worker_test.go`, add a synth counter to the existing `fakeProvider` and new mode/telegram doubles + tests. First, change `fakeProvider` to count calls:

```go
// REPLACE the existing fakeProvider definition with:
type fakeProvider struct {
	format string
	calls  atomic.Int32
}

func (f *fakeProvider) Name() string         { return "fake" }
func (f *fakeProvider) Voices() []string      { return nil }
func (f *fakeProvider) DefaultFormat() string { return f.format }
func (f *fakeProvider) Synthesize(ctx context.Context, req tts.Request) (tts.Audio, error) {
	f.calls.Add(1)
	return tts.Audio{Data: []byte("AUDIO"), Format: f.format}, nil
}
```

Add `"sync/atomic"` to the test imports if not present. **Because `fakeProvider` now embeds an `atomic.Int32`, it must always be used via pointer (`&fakeProvider{...}`) and never copied by value** — `okResolver` and `newModedPool` already construct it with `&`, and `prov.calls.Load()` reads the same instance `newModedPool` returns. Then add the doubles + tests:

```go
type fakeMode struct{ m voicemode.Mode }

func (f fakeMode) Get() voicemode.Mode { return f.m }

type fakeTelegram struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (t *fakeTelegram) SendAudio(ctx context.Context, audio []byte, caption string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.calls++
	return t.err
}
func (t *fakeTelegram) count() int { t.mu.Lock(); defer t.mu.Unlock(); return t.calls }

func newModedPool(mode voicemode.Mode, format string, tg *fakeTelegram, tgReason string) (*WorkerPool, *fakePlayer, *fakeProvider) {
	prov := &fakeProvider{format: format}
	player := &fakePlayer{}
	wp := NewWorkerPool(fakeResolver{prov: prov}, player, 1, 4).WithMode(fakeMode{m: mode})
	if tg != nil {
		wp.WithTelegram(tg, tgReason)
	} else {
		// Pass a literal nil so wp.telegram is a truly-nil interface, not a
		// typed-nil (*fakeTelegram)(nil) that would slip past the nil check.
		wp.WithTelegram(nil, tgReason)
	}
	return wp, player, prov
}

func TestWorkerPool_Mode_Off_SkipsEverything(t *testing.T) {
	wp, player, prov := newModedPool(voicemode.Off, "mp3", &fakeTelegram{}, "")
	wp.Start()
	defer wp.Stop()
	wp.Submit(SpeakRequest{Text: "hi", Profile: "default"})
	waitFor(t, func() bool { return wp.GetStatus().TotalProcessed == 1 }, "job completed")
	if n, _, _ := player.snapshot(); n != 0 {
		t.Errorf("player called %d times in off mode, want 0", n)
	}
	if prov.calls.Load() != 0 {
		t.Errorf("synth called %d times in off mode, want 0 (no cost)", prov.calls.Load())
	}
}

func TestWorkerPool_Mode_Computer_LocalOnly(t *testing.T) {
	tg := &fakeTelegram{}
	wp, player, _ := newModedPool(voicemode.Computer, "mp3", tg, "")
	wp.Start()
	defer wp.Stop()
	wp.Submit(SpeakRequest{Text: "hi", Profile: "default"})
	waitFor(t, func() bool { n, _, _ := player.snapshot(); return n == 1 }, "played locally")
	if tg.count() != 0 {
		t.Errorf("telegram called in computer mode")
	}
}

func TestWorkerPool_Mode_Phone_TelegramOnly(t *testing.T) {
	tg := &fakeTelegram{}
	wp, player, _ := newModedPool(voicemode.Phone, "mp3", tg, "")
	wp.Start()
	defer wp.Stop()
	wp.Submit(SpeakRequest{Text: "hi", Profile: "default"})
	waitFor(t, func() bool { return tg.count() == 1 }, "sent to telegram")
	if n, _, _ := player.snapshot(); n != 0 {
		t.Errorf("player called in phone mode")
	}
}

func TestWorkerPool_Mode_Both(t *testing.T) {
	tg := &fakeTelegram{}
	wp, player, _ := newModedPool(voicemode.Both, "mp3", tg, "")
	wp.Start()
	defer wp.Stop()
	wp.Submit(SpeakRequest{Text: "hi", Profile: "default"})
	waitFor(t, func() bool { n, _, _ := player.snapshot(); return n == 1 && tg.count() == 1 }, "played + sent")
}

func TestWorkerPool_Mode_Both_TelegramErrorStillPlaysLocal(t *testing.T) {
	tg := &fakeTelegram{err: errors.New("telegram down")}
	wp, player, _ := newModedPool(voicemode.Both, "mp3", tg, "")
	wp.Start()
	defer wp.Stop()
	wp.Submit(SpeakRequest{Text: "hi", Profile: "default"})
	waitFor(t, func() bool { n, _, _ := player.snapshot(); return n == 1 }, "played locally despite telegram error")
	if wp.GetStatus().TotalFailed != 0 {
		t.Errorf("both-mode job should not fail when local playback works")
	}
}

func TestWorkerPool_Mode_Phone_TelegramErrorFails(t *testing.T) {
	tg := &fakeTelegram{err: errors.New("telegram down")}
	wp, _, _ := newModedPool(voicemode.Phone, "mp3", tg, "")
	wp.Start()
	defer wp.Stop()
	wp.Submit(SpeakRequest{Text: "hi", Profile: "default"})
	waitFor(t, func() bool { return wp.GetStatus().TotalFailed == 1 }, "phone-only job failed")
}

func TestWorkerPool_Mode_Phone_NotConfiguredFails(t *testing.T) {
	wp, _, _ := newModedPool(voicemode.Phone, "mp3", nil, "set $TELEGRAM_BOT_TOKEN")
	wp.Start()
	defer wp.Stop()
	wp.Submit(SpeakRequest{Text: "hi", Profile: "default"})
	waitFor(t, func() bool { return wp.GetStatus().TotalFailed == 1 }, "failed: telegram not configured")
}

func TestWorkerPool_Mode_Phone_NonMP3Fails(t *testing.T) {
	wp, _, _ := newModedPool(voicemode.Phone, "wav", &fakeTelegram{}, "")
	wp.Start()
	defer wp.Stop()
	wp.Submit(SpeakRequest{Text: "hi", Profile: "default"})
	waitFor(t, func() bool { return wp.GetStatus().TotalFailed == 1 }, "failed: telegram needs mp3")
}
```

Add imports to `worker_test.go` as needed: `"github.com/ybouhjira/claude-code-tts/internal/voicemode"` and `"sync/atomic"`. `errors`, `sync`, `context`, `tts` are already imported.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/server/ -run TestWorkerPool_Mode -v`
Expected: FAIL — `WithMode`/`WithTelegram`/`voicemode` undefined.

- [ ] **Step 3a: Edit `worker.go`**

Add imports: `"github.com/ybouhjira/claude-code-tts/internal/voicemode"`. Add the interfaces (near `synthResolver`):

```go
// modeReader reads the current voice output mode (satisfied by *voicemode.Store).
type modeReader interface {
	Get() voicemode.Mode
}

// telegramSender delivers audio to Telegram (satisfied by *telegram.Sender).
type telegramSender interface {
	SendAudio(ctx context.Context, audio []byte, caption string) error
}
```

Add fields to `WorkerPool` (alongside `resolver`/`player`):

```go
	mode           modeReader     // nil -> always Computer
	telegram       telegramSender // nil -> Telegram unavailable
	telegramReason string         // why telegram is unavailable (for error messages)
```

Add builder methods (after `NewWorkerPool`):

```go
// WithMode sets the voice-mode reader. When unset, the pool behaves as Computer.
func (wp *WorkerPool) WithMode(m modeReader) *WorkerPool { wp.mode = m; return wp }

// WithTelegram sets the Telegram sender (may be nil). reason explains why it is
// unavailable when the sender is nil, for user-facing error messages.
func (wp *WorkerPool) WithTelegram(s telegramSender, reason string) *WorkerPool {
	wp.telegram = s
	wp.telegramReason = reason
	return wp
}
```

Replace `processJob` (from `job.mu.Lock(); job.Status = "processing"` through the end of the synth/play section) with the mode-aware version. The full new `processJob`:

```go
func (wp *WorkerPool) processJob(job *Job) {
	startTime := time.Now()

	mode := voicemode.Computer
	if wp.mode != nil {
		mode = wp.mode.Get()
	}
	logging.Info("Job %s: starting (mode=%s, profile=%s, provider=%s, text_len=%d)", job.ID, mode, job.Profile, job.Provider, len(job.Text))

	job.mu.Lock()
	job.Status = "processing"
	job.mu.Unlock()

	// Off: no synthesis (no cost), no delivery.
	if mode == voicemode.Off {
		job.mu.Lock()
		job.Status = "completed"
		job.mu.Unlock()
		wp.processed.Add(1)
		logging.Info("Job %s: muted (voice mode off)", job.ID)
		return
	}

	var provider tts.Provider
	var req tts.Request
	var err error
	switch {
	case job.Provider != "":
		provider, req, err = wp.resolver.ResolveVoice(job.Provider, job.Voice, job.Speed)
	case job.Profile != "":
		provider, req, err = wp.resolver.Resolve(job.Profile)
	default:
		provider, req, err = wp.resolver.Default()
	}
	if err != nil {
		wp.failJob(job, fmt.Errorf("resolve: %w", err), startTime)
		return
	}
	if job.Voice != "" {
		req.Voice = job.Voice
	}
	if job.Speed != 0 {
		req.Speed = job.Speed
	}
	req.Text = job.Text

	// Fail fast on Telegram misconfiguration before paying for synthesis.
	if mode.SendsTelegram() {
		if wp.telegram == nil {
			wp.failJob(job, fmt.Errorf("Telegram not configured: %s", wp.telegramReason), startTime)
			return
		}
		if provider.DefaultFormat() != "mp3" {
			wp.failJob(job, fmt.Errorf("Telegram requires an MP3 provider (OpenAI or Grok); %q emits %s", provider.Name(), provider.DefaultFormat()), startTime)
			return
		}
	}

	audioOut, err := provider.Synthesize(context.Background(), req)
	if err != nil {
		wp.failJob(job, fmt.Errorf("synthesis: %w", err), startTime)
		return
	}

	if mode.SendsTelegram() {
		if sendErr := wp.telegram.SendAudio(context.Background(), audioOut.Data, job.Text); sendErr != nil {
			if !mode.PlaysLocal() {
				wp.failJob(job, fmt.Errorf("telegram: %w", sendErr), startTime)
				return
			}
			logging.Error("Job %s: telegram send failed (continuing to local): %v", job.ID, sendErr)
		}
	}

	if mode.PlaysLocal() {
		if err := wp.player.Play(audioOut.Data, audioOut.Format); err != nil {
			wp.failJob(job, fmt.Errorf("playback: %w", err), startTime)
			return
		}
	}

	job.mu.Lock()
	job.Status = "completed"
	job.mu.Unlock()
	wp.processed.Add(1)
	logging.Info("Job %s: completed in %v", job.ID, time.Since(startTime))
}
```

Add two fields to the `PoolStatus` struct, right after `RecentJobs []*Job ...`:

```go
	VoiceMode          string `json:"voice_mode"`
	TelegramConfigured bool   `json:"telegram_configured"`
```

In `GetStatus`, compute the mode immediately after `wp.historyMu.RUnlock()` and before `return PoolStatus{...}`:

```go
	mode := voicemode.Computer
	if wp.mode != nil {
		mode = wp.mode.Get()
	}
```

and add these two lines inside the returned `PoolStatus{...}` literal (after `RecentJobs: recentJobs,`):

```go
		VoiceMode:          string(mode),
		TelegramConfigured: wp.telegram != nil,
```

- [ ] **Step 3b: Edit `server.go`** — wire the mode store + telegram sender and add the `tts_output` tool.

Add imports: `"github.com/ybouhjira/claude-code-tts/internal/voicemode"`. Add a field to `Server`:

```go
type Server struct {
	mcpServer  *server.MCPServer
	workerPool *WorkerPool
	modeStore  *voicemode.Store
}
```

Change `New()` to build the mode store + telegram sender and wire them:

```go
func New() (*Server, error) {
	logging.Info("Creating TTS MCP server...")

	reg := ttsconfig.LoadOrDefault()
	player := audio.NewPlayer()
	modeStore := voicemode.DefaultStore()
	tgSender, tgReason := reg.TelegramSender()

	wp := NewWorkerPool(reg, player, 2, 50).
		WithMode(modeStore)
	if tgSender != nil {
		wp.WithTelegram(tgSender, "")
	} else {
		wp.WithTelegram(nil, tgReason)
	}
	wp.Start()

	mcpSrv := server.NewMCPServer("claude-code-tts", "1.0.0", server.WithToolCapabilities(true))
	s := &Server{mcpServer: mcpSrv, workerPool: wp, modeStore: modeStore}
	s.registerTools()
	return s, nil
}
```

In `registerTools()`, register a new tool:

```go
	outputTool := mcp.NewTool("tts_output",
		mcp.WithDescription("Set where Claude's voice goes. Call this when the user asks to turn the voice on/off or change where it plays — e.g. \"turn the voice off\", \"speak out loud\", \"send the voice to my phone\", \"use both\"."),
		mcp.WithString("mode", mcp.Required(),
			mcp.Description("One of: off (silent), computer (this PC's speakers), phone (Telegram), both.")),
	)
	s.mcpServer.AddTool(outputTool, s.handleSetOutput)
```

Add the handler:

```go
func (s *Server) handleSetOutput(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	m, _ := request.Params.Arguments["mode"].(string)
	mode := voicemode.Mode(m)
	if !voicemode.Valid(mode) {
		return mcp.NewToolResultError("invalid mode; use one of: off, computer, phone, both"), nil
	}
	if err := s.modeStore.Set(mode); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to set voice output: %v", err)), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf("Voice output set to: %s", mode)), nil
}
```

(`fmt` and `context` are already imported in `server.go`.)

- [ ] **Step 3c: Add a server test** to `internal/server/server_test.go`:

```go
func TestHandleSetOutput(t *testing.T) {
	dir := t.TempDir()
	store := voicemode.NewStore(filepath.Join(dir, "state.json"))
	wp := NewWorkerPool(okResolver("mp3"), &fakePlayer{}, 1, 4).WithMode(store)
	wp.Start()
	t.Cleanup(wp.Stop)
	s := &Server{workerPool: wp, modeStore: store}

	res, err := s.handleSetOutput(context.Background(), speakReq(map[string]any{"mode": "phone"}))
	if err != nil || res.IsError {
		t.Fatalf("handleSetOutput: %v / %+v", err, res)
	}
	if store.Get() != voicemode.Phone {
		t.Errorf("mode = %q, want phone", store.Get())
	}

	// tts_status must reflect the new mode (json.MarshalIndent renders "key": "value").
	st, _ := s.handleStatus(context.Background(), speakReq(nil))
	if !strings.Contains(st.Content[0].(mcp.TextContent).Text, `"voice_mode": "phone"`) {
		t.Errorf("tts_status missing voice_mode=phone:\n%s", st.Content[0].(mcp.TextContent).Text)
	}

	bad, _ := s.handleSetOutput(context.Background(), speakReq(map[string]any{"mode": "loud"}))
	if !bad.IsError {
		t.Error("expected error for invalid mode")
	}
}
```

Add imports to `server_test.go`: `"path/filepath"` and `"github.com/ybouhjira/claude-code-tts/internal/voicemode"`.

- [ ] **Step 4: Verify**

Run:
```
go vet ./internal/server/
go test ./internal/server/ -v
go build ./cmd/tts-server/
```
Expected: vet clean; all server tests (existing + new mode tests) pass; tts-server builds.

- [ ] **Step 5: Commit**

```bash
git add internal/server/
git commit -m "feat(server): mode-aware delivery (local/telegram) + tts_output tool"
```

---

## Task 5: CLI — `mode`/`status` subcommands + `-to` flag

**Files:**
- Modify: `cmd/speak-text/main.go`

- [ ] **Step 1: Replace `cmd/speak-text/main.go`**

(No unit test — thin `main`; the logic it calls is covered by the `voicemode`/`telegram`/`ttsconfig` package tests. Verified by build + a runtime round-trip in Step 2.)

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
	"github.com/ybouhjira/claude-code-tts/internal/voicemode"
)

func main() {
	args := os.Args[1:]
	switch {
	case len(args) >= 1 && args[0] == "mode":
		runMode(args[1:])
	case len(args) >= 1 && args[0] == "status":
		runStatus()
	default:
		runSpeak(args)
	}
}

func runMode(args []string) {
	store := voicemode.DefaultStore()
	if len(args) == 0 {
		fmt.Printf("voice mode: %s\n", store.Get())
		return
	}
	m := voicemode.Mode(args[0])
	if err := store.Set(m); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("voice mode set to: %s\n", m)
}

func runStatus() {
	fmt.Printf("voice mode: %s\n", voicemode.DefaultStore().Get())
	if s, reason := ttsconfig.LoadOrDefault().TelegramSender(); s != nil {
		fmt.Println("telegram: configured")
	} else {
		fmt.Printf("telegram: not configured (%s)\n", reason)
	}
}

func runSpeak(args []string) {
	fs := flag.NewFlagSet("speak-text", flag.ExitOnError)
	to := fs.String("to", "computer", "Where to deliver THIS clip: computer, phone, or both")
	providerFlag := fs.String("provider", "", "Use an explicit provider (openai, grok, piper) instead of a profile")
	profile := fs.String("profile", "", "Voice profile from config (default: configured default / CLAUDE_TTS_* env)")
	voice := fs.String("voice", "", "Override the profile's voice")
	speed := fs.Float64("speed", 0, "Override speech speed (0 = profile default)")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage:\n")
		fmt.Fprintf(os.Stderr, "  %s [OPTIONS] TEXT        speak text now\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s mode [off|computer|phone|both]   show or set the saved voice mode\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s status                show voice mode + telegram status\n\n", os.Args[0])
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)
	if fs.NArg() == 0 {
		fs.Usage()
		os.Exit(1)
	}
	text := fs.Arg(0)

	dest := voicemode.Mode(*to)
	if dest != voicemode.Computer && dest != voicemode.Phone && dest != voicemode.Both {
		fmt.Fprintf(os.Stderr, "Error: -to must be computer, phone, or both\n")
		os.Exit(1)
	}

	reg := ttsconfig.LoadOrDefault()

	var prov tts.Provider
	var req tts.Request
	var err error
	switch {
	case *providerFlag != "":
		prov, req, err = reg.ResolveVoice(*providerFlag, *voice, *speed)
	case *profile != "":
		prov, req, err = reg.Resolve(*profile)
	default:
		prov, req, err = reg.Default()
	}
	if err == nil {
		if *voice != "" {
			req.Voice = *voice
		}
		if *speed != 0 {
			req.Speed = *speed
		}
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	req.Text = text

	if dest.SendsTelegram() && prov.DefaultFormat() != "mp3" {
		fmt.Fprintf(os.Stderr, "Error: telegram needs an MP3 provider (OpenAI or Grok), but %q emits %s\n", prov.Name(), prov.DefaultFormat())
		os.Exit(1)
	}

	out, err := prov.Synthesize(context.Background(), req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error synthesizing speech: %v\n", err)
		os.Exit(1)
	}

	if dest.SendsTelegram() {
		sender, reason := reg.TelegramSender()
		if sender == nil {
			fmt.Fprintf(os.Stderr, "Error: telegram not configured (%s)\n", reason)
			os.Exit(1)
		}
		if err := sender.SendAudio(context.Background(), out.Data, text); err != nil {
			fmt.Fprintf(os.Stderr, "Error sending to telegram: %v\n", err)
			os.Exit(1)
		}
	}
	if dest.PlaysLocal() {
		if err := audio.NewPlayer().Play(out.Data, out.Format); err != nil {
			fmt.Fprintf(os.Stderr, "Error playing audio: %v\n", err)
			os.Exit(1)
		}
	}
}
```

- [ ] **Step 2: Build and round-trip the mode subcommand**

Run:
```
go build ./cmd/speak-text/
go vet ./cmd/speak-text/
```
Then verify the mode round-trip with an isolated state file (bash):
```
CLAUDE_TTS_STATE="$(mktemp -d)/state.json" sh -c 'go run ./cmd/speak-text mode phone && go run ./cmd/speak-text mode'
```
Expected: prints `voice mode set to: phone` then `voice mode: phone`.

- [ ] **Step 3: Commit**

```bash
git add cmd/speak-text/main.go
git commit -m "feat(cli): mode/status subcommands and -to delivery flag"
```

---

## Task 6: Example config, docs, full build/test

**Files:**
- Modify: `config.example.json`, `README.md`, `CLAUDE.md`

- [ ] **Step 1: Add the telegram section to `config.example.json`**

Add a top-level `"telegram"` key (place it after `"profiles"`):

```json
  "telegram": { "bot_token_env": "TELEGRAM_BOT_TOKEN", "chat_id": "REPLACE_WITH_YOUR_CHAT_ID" }
```

Ensure the surrounding JSON stays valid (add the comma after the `profiles` object).

- [ ] **Step 2: Document in `README.md` and `CLAUDE.md`**

Add a "Voice output & Telegram" section to `README.md` covering: the four modes (`off`/`computer`/`phone`/`both`, default `computer`); setting it from chat (the `tts_output` tool — just ask Claude) or the CLI (`speak-text mode phone`, `speak-text status`); the one-time Telegram setup (`TELEGRAM_BOT_TOKEN` env + `telegram.chat_id` in config.json; how to find the chat id via `https://api.telegram.org/bot<token>/getUpdates` after messaging the bot once); the `-to` flag for one-off CLI delivery; and the note that Telegram needs an MP3 provider (OpenAI/Grok), not Piper. In `CLAUDE.md`, add `tts_output` to the MCP tools table and mention the `voicemode` + `telegram` packages in the architecture section.

- [ ] **Step 3: Format, build, vet, and full test**

Run:
```
gofmt -w .
go build ./...
go vet ./...
go test ./...
```
Expected: all build and pass (note: `go test ./...` may take ~30–60s because the audio package exercises the real player; it should not hang). Fix any leftover compile issues the compiler surfaces.

- [ ] **Step 4: Commit**

```bash
git add config.example.json README.md CLAUDE.md
git commit -m "docs: document voice mode + telegram delivery"
```

---

## Self-Review Notes (for the implementer)

- **Spec coverage:** voice mode store (Task 1, spec §5.1); telegram sender (Task 2, §5.2); telegram config + builder (Task 3, §5.3); mode-aware worker routing + `tts_output` + `tts_status.voice_mode` (Task 4, §5.4/§5.5); CLI mode/status/`-to` (Task 5, §5.6); error handling — off-no-synth, not-configured, non-mp3, phone-fails-vs-both-best-effort (Task 4, §7); back-compat default Computer via nil mode (Task 4, §8); docs + example (Task 6, §10/§11).
- **Type consistency:** `voicemode.Mode` (`Off`/`Computer`/`Phone`/`Both`), `Valid`, `(Mode).PlaysLocal()`/`SendsTelegram()`, `Store.Get()/Set()` used identically across worker, server, CLI. `telegram.Sender.SendAudio(ctx, []byte, string) error` matches the `telegramSender` interface in worker.go and the `fakeTelegram` mock. `(*Registry).TelegramSender() (*telegram.Sender, string)` consumed by `server.New()` and the CLI. `WithMode`/`WithTelegram` builder methods are nil-safe so existing `NewWorkerPool(...)` callers and tests are unchanged.
- **Coupling:** Task 4 edits `worker.go` + `server.go` together (one package); verify with `go test ./internal/server/` + `go build ./cmd/tts-server/`. Tasks 1–3 and 5 are independent packages.
- **Out of scope:** OGG/Opus `sendVoice` bubble (ffmpeg), Piper-over-Telegram, the relay/PWA/auto-speak path, inbound Telegram control.
