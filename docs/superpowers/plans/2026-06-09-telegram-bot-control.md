# Interactive Telegram Bot Control + Cost Info Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the Telegram bot interactive — `/voices` (audio demos + tap-to-select), `/model`, `/menu`, `/help` driven by a background poller in `tts-server` — persist the chosen voice/model so Claude speaks with it, and stamp every voice message with an estimated cost + model + voice.

**Architecture:** A `botcontrol.Poller` long-polls Telegram `getUpdates` in a goroutine started by `tts-server`. Commands/button-taps update a persisted `voicemode.SettingsStore` (voice + model). The worker/CLI apply those overrides on top of the resolved profile and attach a cost caption (`internal/cost`) to Telegram sends. Only the configured `chat_id` is honored.

**Tech Stack:** Go (per `go.mod`, 1.23; local toolchain may be newer), stdlib `net/http`+`encoding/json`+`mime/multipart`, the existing `tts`/`ttsconfig`/`telegram`/`voicemode` packages. No new modules.

**Spec:** `docs/superpowers/specs/2026-06-09-telegram-bot-control-design.md`
**Module path:** `github.com/ybouhjira/claude-code-tts`

**Convention:** When a pasted code block starts with a `// path/file.go` comment, OMIT that line — start at `package <name>`. Run `gofmt -w` on changed files before committing.

---

## File Structure

**New**
- `internal/cost/cost.go` (+ `_test.go`) — price table; `CentsFor`, `EffectiveModel`, `ModelsFor`.
- `internal/voicemode/settings.go` (+ `settings_test.go`) — `Settings`, `SettingsStore`.
- `internal/botcontrol/poller.go` (+ `poller_test.go`) — the command/callback handler + poll loop.

**Modified**
- `internal/telegram/telegram.go` (+ `_test.go`) — `Update`/`Message`/`Chat`/`CallbackQuery`/`InlineButton` types; `GetUpdates`, `AnswerCallback`, `SendMessage`, `SendVoiceWithButton`; `sendFile` gains a keyboard arg.
- `internal/server/worker.go` (+ `worker_test.go`) — `WithSettings` (nil-safe); apply voice/model overrides; build the cost caption for Telegram.
- `internal/server/server.go` — wire the settings store, start/stop the poller, build the registry-backed source.
- `cmd/speak-text/main.go` — apply settings overrides + caption on `-to phone/both`.
- `README.md` / `CLAUDE.md` — document the bot commands + caption.

**Coupling:** Task 7 edits `server.go` only; Task 4 edits `worker.go` only — both compile independently because the worker uses a nil-safe `WithSettings` builder (existing `NewWorkerPool` callers/tests are unaffected). Tasks 1–3 are standalone packages.

---

## Task 1: `cost` package

**Files:**
- Create: `internal/cost/cost.go`
- Test: `internal/cost/cost_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/cost/cost_test.go
package cost

import (
	"math"
	"testing"
)

func TestCentsFor(t *testing.T) {
	// tts-1 = $15 / 1M chars; 1000 chars -> $0.015 -> 1.5 cents.
	if got := CentsFor("openai", "tts-1", 1000); math.Abs(got-1.5) > 1e-9 {
		t.Errorf("tts-1 1000 chars = %v cents, want 1.5", got)
	}
	// tts-1-hd = $30 / 1M chars; 1000 chars -> 3.0 cents.
	if got := CentsFor("openai", "tts-1-hd", 1000); math.Abs(got-3.0) > 1e-9 {
		t.Errorf("tts-1-hd 1000 chars = %v cents, want 3.0", got)
	}
	// Empty model uses the provider default (tts-1).
	if got := CentsFor("openai", "", 1000); math.Abs(got-1.5) > 1e-9 {
		t.Errorf("openai default 1000 chars = %v cents, want 1.5", got)
	}
	// Unknown provider/model -> 0.
	if got := CentsFor("nope", "x", 1000); got != 0 {
		t.Errorf("unknown = %v, want 0", got)
	}
}

func TestEffectiveModel(t *testing.T) {
	if EffectiveModel("openai", "") != "tts-1" {
		t.Errorf("openai default = %q, want tts-1", EffectiveModel("openai", ""))
	}
	if EffectiveModel("openai", "tts-1-hd") != "tts-1-hd" {
		t.Errorf("explicit model not preserved")
	}
	if EffectiveModel("grok", "") != "grok" {
		t.Errorf("grok default = %q, want grok", EffectiveModel("grok", ""))
	}
}

func TestModelsFor(t *testing.T) {
	if got := ModelsFor("openai"); len(got) != 3 || got[0] != "tts-1" {
		t.Errorf("openai models = %v", got)
	}
	if got := ModelsFor("nope"); got != nil {
		t.Errorf("unknown models = %v, want nil", got)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/cost/ -v`
Expected: FAIL — package/funcs undefined.

- [ ] **Step 3: Write the implementation**

```go
// internal/cost/cost.go
package cost

// usdPerMillionChars holds approximate published TTS prices in USD per 1,000,000
// characters. These are estimates for showing rough spend, not exact billing
// (gpt-4o-mini-tts is token-priced upstream; the value here approximates it).
var usdPerMillionChars = map[string]map[string]float64{
	"openai": {"tts-1": 15.0, "tts-1-hd": 30.0, "gpt-4o-mini-tts": 12.0},
	"grok":   {"grok": 5.0},
}

// defaultModel is the model name assumed when a provider is used without an
// explicit model.
var defaultModel = map[string]string{
	"openai": "tts-1",
	"grok":   "grok",
	"piper":  "piper",
}

// EffectiveModel returns model, or the provider's default when model is empty.
func EffectiveModel(provider, model string) string {
	if model != "" {
		return model
	}
	return defaultModel[provider]
}

// CentsFor estimates the cost in cents of synthesizing chars characters with the
// given provider+model. An empty model uses the provider default. Unknown
// provider/model returns 0.
func CentsFor(provider, model string, chars int) float64 {
	rates, ok := usdPerMillionChars[provider]
	if !ok {
		return 0
	}
	rate, ok := rates[EffectiveModel(provider, model)]
	if !ok {
		return 0
	}
	return float64(chars) * rate / 1_000_000.0 * 100.0
}

// ModelsFor returns the models offered for a provider (stable order), or nil.
func ModelsFor(provider string) []string {
	switch provider {
	case "openai":
		return []string{"tts-1", "tts-1-hd", "gpt-4o-mini-tts"}
	case "grok":
		return []string{"grok"}
	}
	return nil
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/cost/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cost/
git commit -m "feat(cost): per-model cost estimate + model list"
```

---

## Task 2: `voicemode.SettingsStore`

**Files:**
- Create: `internal/voicemode/settings.go`
- Test: `internal/voicemode/settings_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/voicemode/settings_test.go
package voicemode

import (
	"path/filepath"
	"testing"
)

func TestSettingsStore_DefaultEmpty(t *testing.T) {
	s := NewSettingsStore(filepath.Join(t.TempDir(), "vs.json"))
	got := s.Get()
	if got.Voice != "" || got.Model != "" {
		t.Errorf("missing file -> %+v, want empty", got)
	}
}

func TestSettingsStore_SetGet(t *testing.T) {
	s := NewSettingsStore(filepath.Join(t.TempDir(), "vs.json"))
	if err := s.SetVoice("onyx"); err != nil {
		t.Fatalf("SetVoice: %v", err)
	}
	if err := s.SetModel("tts-1-hd"); err != nil {
		t.Fatalf("SetModel: %v", err)
	}
	got := s.Get()
	if got.Voice != "onyx" || got.Model != "tts-1-hd" {
		t.Errorf("got %+v, want onyx/tts-1-hd", got)
	}
	// A fresh store over the same file reads the persisted values.
	if got2 := NewSettingsStore(s.path).Get(); got2.Voice != "onyx" || got2.Model != "tts-1-hd" {
		t.Errorf("reload got %+v", got2)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/voicemode/ -run TestSettingsStore -v`
Expected: FAIL — `NewSettingsStore` undefined.

- [ ] **Step 3: Write the implementation**

```go
// internal/voicemode/settings.go
package voicemode

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Settings is the user's persisted voice/model selection (empty = profile default).
type Settings struct {
	Voice string `json:"voice"`
	Model string `json:"model"`
}

// SettingsStore persists Settings to a JSON file.
type SettingsStore struct {
	path string
	mu   sync.Mutex
}

// NewSettingsStore creates a store backed by the file at path.
func NewSettingsStore(path string) *SettingsStore { return &SettingsStore{path: path} }

// DefaultSettingsStore uses ~/.claude/plugins/claude-code-tts/voice-settings.json
// (overridable via CLAUDE_TTS_SETTINGS).
func DefaultSettingsStore() *SettingsStore { return NewSettingsStore(settingsPath()) }

func settingsPath() string {
	if p := os.Getenv("CLAUDE_TTS_SETTINGS"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "plugins", "claude-code-tts", "voice-settings.json")
}

// Get returns the persisted settings, or zero values when the file is missing
// or unreadable.
func (s *SettingsStore) Get() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if err != nil {
		return Settings{}
	}
	var st Settings
	if err := json.Unmarshal(data, &st); err != nil {
		return Settings{}
	}
	return st
}

// SetVoice updates the voice, preserving the model.
func (s *SettingsStore) SetVoice(v string) error { return s.update(func(st *Settings) { st.Voice = v }) }

// SetModel updates the model, preserving the voice.
func (s *SettingsStore) SetModel(m string) error { return s.update(func(st *Settings) { st.Model = m }) }

func (s *SettingsStore) update(mut func(*Settings)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var st Settings
	if data, err := os.ReadFile(s.path); err == nil {
		_ = json.Unmarshal(data, &st)
	}
	mut(&st)
	if dir := filepath.Dir(s.path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("voicemode: create dir: %w", err)
		}
	}
	data, _ := json.Marshal(st)
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("voicemode: write settings: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("voicemode: rename settings: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/voicemode/ -v`
Expected: PASS (settings + existing mode tests).

- [ ] **Step 5: Commit**

```bash
git add internal/voicemode/settings.go internal/voicemode/settings_test.go
git commit -m "feat(voicemode): persisted voice/model selection store"
```

---

## Task 3: Telegram API additions (updates, callbacks, messages, inline keyboards)

**Files:**
- Modify: `internal/telegram/telegram.go`
- Test: `internal/telegram/telegram_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// add to internal/telegram/telegram_test.go
func TestGetUpdates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/botT/getUpdates" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"result":[
			{"update_id":10,"message":{"text":"/voices","chat":{"id":42}}},
			{"update_id":11,"callback_query":{"id":"cb1","data":"voice:onyx","message":{"chat":{"id":42}}}}
		]}`))
	}))
	defer srv.Close()

	s := NewSender("T", "42")
	s.baseURL = srv.URL
	ups, err := s.GetUpdates(context.Background(), 0, 0)
	if err != nil {
		t.Fatalf("GetUpdates: %v", err)
	}
	if len(ups) != 2 {
		t.Fatalf("got %d updates, want 2", len(ups))
	}
	if ups[0].Message == nil || ups[0].Message.Text != "/voices" || ups[0].Message.Chat.ID != 42 {
		t.Errorf("update 0 = %+v", ups[0])
	}
	if ups[1].CallbackQuery == nil || ups[1].CallbackQuery.Data != "voice:onyx" {
		t.Errorf("update 1 = %+v", ups[1])
	}
}

func TestSendMessage_WithKeyboard(t *testing.T) {
	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	s := NewSender("T", "42")
	s.baseURL = srv.URL
	kb := [][]InlineButton{{{Text: "Use onyx", CallbackData: "voice:onyx"}}}
	if err := s.SendMessage(context.Background(), "pick", kb); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if gotPath != "/botT/sendMessage" {
		t.Errorf("path = %q", gotPath)
	}
	if !strings.Contains(gotBody, `"callback_data":"voice:onyx"`) || !strings.Contains(gotBody, `"chat_id"`) {
		t.Errorf("body missing keyboard/chat_id: %s", gotBody)
	}
}

func TestAnswerCallback(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	s := NewSender("T", "42")
	s.baseURL = srv.URL
	if err := s.AnswerCallback(context.Background(), "cb1", "done"); err != nil {
		t.Fatalf("AnswerCallback: %v", err)
	}
	if gotPath != "/botT/answerCallbackQuery" {
		t.Errorf("path = %q", gotPath)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/telegram/ -run 'TestGetUpdates|TestSendMessage_WithKeyboard|TestAnswerCallback' -v`
Expected: FAIL — types/methods undefined.

- [ ] **Step 3a: Add the types** to `internal/telegram/telegram.go` (after the `Sender` struct):

```go
// Update is the subset of a Telegram update this package consumes.
type Update struct {
	UpdateID      int            `json:"update_id"`
	Message       *Message       `json:"message,omitempty"`
	CallbackQuery *CallbackQuery `json:"callback_query,omitempty"`
}

// Message is a chat message.
type Message struct {
	Text string `json:"text"`
	Chat Chat   `json:"chat"`
}

// Chat identifies a chat.
type Chat struct {
	ID int64 `json:"id"`
}

// CallbackQuery is an inline-button tap.
type CallbackQuery struct {
	ID      string   `json:"id"`
	Data    string   `json:"data"`
	Message *Message `json:"message,omitempty"`
}

// InlineButton is one inline-keyboard button.
type InlineButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}
```

- [ ] **Step 3b: Add the methods** to `internal/telegram/telegram.go`. Add `"net/url"` and `"strconv"` to the imports.

```go
// GetUpdates long-polls for new updates starting at offset. timeoutSecs is the
// long-poll timeout (0 = immediate). Use the highest received UpdateID+1 as the
// next offset to acknowledge processed updates.
func (s *Sender) GetUpdates(ctx context.Context, offset, timeoutSecs int) ([]Update, error) {
	q := url.Values{}
	q.Set("offset", strconv.Itoa(offset))
	q.Set("timeout", strconv.Itoa(timeoutSecs))
	u := fmt.Sprintf("%s/bot%s/getUpdates?%s", s.baseURL, s.token, q.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("telegram: create request: %s", s.redact(err.Error()))
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("telegram: getUpdates failed: %s", s.redact(err.Error()))
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("telegram: getUpdates error (status %d): %s", resp.StatusCode, s.redact(string(b)))
	}
	var out struct {
		OK     bool     `json:"ok"`
		Result []Update `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("telegram: decode updates: %w", err)
	}
	return out.Result, nil
}

// SendMessage posts a text message with an optional inline keyboard.
func (s *Sender) SendMessage(ctx context.Context, text string, keyboard [][]InlineButton) error {
	form := url.Values{}
	form.Set("chat_id", s.chatID)
	form.Set("text", text)
	if len(keyboard) > 0 {
		markup, _ := json.Marshal(map[string]any{"inline_keyboard": keyboard})
		form.Set("reply_markup", string(markup))
	}
	return s.postForm(ctx, "sendMessage", form)
}

// AnswerCallback acknowledges a button tap (clears the loading spinner) and
// optionally shows a short toast.
func (s *Sender) AnswerCallback(ctx context.Context, callbackID, text string) error {
	form := url.Values{}
	form.Set("callback_query_id", callbackID)
	if text != "" {
		form.Set("text", text)
	}
	return s.postForm(ctx, "answerCallbackQuery", form)
}

// SendVoiceWithButton sends an Opus voice message with an inline keyboard.
func (s *Sender) SendVoiceWithButton(ctx context.Context, audio []byte, caption string, keyboard [][]InlineButton) error {
	return s.sendFile(ctx, "sendVoice", "voice", "clip.ogg", audio, caption, keyboard)
}

// postForm POSTs an application/x-www-form-urlencoded request to a Bot API method.
func (s *Sender) postForm(ctx context.Context, method string, form url.Values) error {
	u := fmt.Sprintf("%s/bot%s/%s", s.baseURL, s.token, method)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("telegram: create request: %s", s.redact(err.Error()))
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("telegram: %s failed: %s", method, s.redact(err.Error()))
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("telegram: %s error (status %d): %s", method, resp.StatusCode, s.redact(string(b)))
	}
	return nil
}
```

- [ ] **Step 3c: Thread a keyboard through `sendFile`.** Change the `sendFile` signature and its two callers (`Send`, `SendAudio`), and add the `reply_markup` field:

```go
// Send (unchanged routing) now passes nil keyboard:
func (s *Sender) Send(ctx context.Context, audio []byte, format, caption string) error {
	if format == "opus" {
		return s.sendFile(ctx, "sendVoice", "voice", "clip.ogg", audio, caption, nil)
	}
	return s.sendFile(ctx, "sendAudio", "audio", "clip.mp3", audio, caption, nil)
}

// SendAudio passes nil keyboard:
func (s *Sender) SendAudio(ctx context.Context, audio []byte, caption string) error {
	return s.sendFile(ctx, "sendAudio", "audio", "clip.mp3", audio, caption, nil)
}

// sendFile gains a keyboard parameter; add this block right after the optional
// caption field, before CreateFormFile:
//     if len(keyboard) > 0 {
//         markup, _ := json.Marshal(map[string]any{"inline_keyboard": keyboard})
//         _ = w.WriteField("reply_markup", string(markup))
//     }
func (s *Sender) sendFile(ctx context.Context, method, field, filename string, data []byte, caption string, keyboard [][]InlineButton) error {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	_ = w.WriteField("chat_id", s.chatID)
	if caption != "" {
		_ = w.WriteField("caption", caption)
	}
	if len(keyboard) > 0 {
		markup, _ := json.Marshal(map[string]any{"inline_keyboard": keyboard})
		_ = w.WriteField("reply_markup", string(markup))
	}
	part, err := w.CreateFormFile(field, filename)
	if err != nil {
		return fmt.Errorf("telegram: create form file: %w", err)
	}
	if _, err := part.Write(data); err != nil {
		return fmt.Errorf("telegram: write %s: %w", field, err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("telegram: close writer: %w", err)
	}

	url := fmt.Sprintf("%s/bot%s/%s", s.baseURL, s.token, method)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &body)
	if err != nil {
		return fmt.Errorf("telegram: create request: %s", s.redact(err.Error()))
	}
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("telegram: request failed: %s", s.redact(err.Error()))
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("telegram: API error (status %d): %s", resp.StatusCode, s.redact(string(b)))
	}
	return nil
}
```

Add `"encoding/json"` to the imports (used by the keyboard marshal).

- [ ] **Step 4: Run the package tests**

Run: `go test ./internal/telegram/ -v`
Expected: PASS (new + existing tests, incl. the token-redaction and Send routing tests).

- [ ] **Step 5: Commit**

```bash
git add internal/telegram/
git commit -m "feat(telegram): getUpdates, sendMessage, answerCallback, inline keyboards"
```

---

## Task 4: Worker — apply voice/model overrides + cost caption

**Files:**
- Modify: `internal/server/worker.go`
- Modify: `internal/server/worker_test.go`

- [ ] **Step 1: Add worker tests**

```go
// add to internal/server/worker_test.go
type fakeSettings struct{ s voicemode.Settings }

func (f fakeSettings) Get() voicemode.Settings { return f.s }

func TestWorkerPool_AppliesSettingsOverride(t *testing.T) {
	tg := &fakeTelegram{}
	prov := &fakeProvider{format: "mp3"}
	player := &fakePlayer{}
	wp := NewWorkerPool(fakeResolver{prov: prov}, player, 1, 4).
		WithMode(fakeMode{m: voicemode.Both}).
		WithTelegram(tg, "").
		WithSettings(fakeSettings{s: voicemode.Settings{Voice: "onyx", Model: "tts-1-hd"}})
	wp.Start()
	defer wp.Stop()
	// job leaves voice/model empty -> settings override applies.
	wp.Submit(SpeakRequest{Text: "hi", Profile: "default"})
	waitFor(t, func() bool { return tg.count() == 1 }, "sent")
	// The fakeResolver returns Voice:"v"; the override must replace it with onyx.
	if gotV := tg.captionHas("onyx"); !gotV {
		t.Errorf("telegram caption %q missing overridden voice onyx", tg.lastCaption())
	}
}

func TestWorkerPool_ExplicitVoiceBeatsSettings(t *testing.T) {
	tg := &fakeTelegram{}
	prov := &fakeProvider{format: "mp3"}
	wp := NewWorkerPool(fakeResolver{prov: prov}, &fakePlayer{}, 1, 4).
		WithMode(fakeMode{m: voicemode.Phone}).
		WithTelegram(tg, "").
		WithSettings(fakeSettings{s: voicemode.Settings{Voice: "onyx"}})
	wp.Start()
	defer wp.Stop()
	wp.Submit(SpeakRequest{Text: "hi", Provider: "fake", Voice: "echo"}) // explicit voice
	waitFor(t, func() bool { return tg.count() == 1 }, "sent")
	if !tg.captionHas("echo") {
		t.Errorf("caption %q should keep explicit voice echo", tg.lastCaption())
	}
}
```

Extend the existing `fakeTelegram` (in `worker_test.go`) to record the caption — replace its `Send` method and add helpers:

```go
func (t *fakeTelegram) Send(ctx context.Context, audio []byte, format, caption string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.calls++
	t.lastFormat = format
	t.lastCap = caption
	return t.err
}
func (t *fakeTelegram) lastCaption() string { t.mu.Lock(); defer t.mu.Unlock(); return t.lastCap }
func (t *fakeTelegram) captionHas(s string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Contains(t.lastCap, s)
}
```

Add `lastCap string` to the `fakeTelegram` struct, and add `"strings"` + `"github.com/ybouhjira/claude-code-tts/internal/voicemode"` to the test imports if not present.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/server/ -run 'TestWorkerPool_AppliesSettingsOverride|TestWorkerPool_ExplicitVoiceBeatsSettings' -v`
Expected: FAIL — `WithSettings` undefined.

- [ ] **Step 3: Edit `worker.go`**

Add the imports `"github.com/ybouhjira/claude-code-tts/internal/cost"` and `"github.com/ybouhjira/claude-code-tts/internal/voicemode"` (voicemode may already be imported). Add the interface + field + builder:

```go
// settingsReader reads the persisted voice/model selection (satisfied by
// *voicemode.SettingsStore).
type settingsReader interface {
	Get() voicemode.Settings
}
```

Add a field to `WorkerPool`: `settings settingsReader // nil -> no override`. Add a builder:

```go
// WithSettings sets the persisted voice/model selection source (nil-safe).
func (wp *WorkerPool) WithSettings(s settingsReader) *WorkerPool { wp.settings = s; return wp }
```

In `processJob`, after the existing override block (`if job.Voice != "" { req.Voice = job.Voice }` … `req.Text = job.Text`), insert the settings override (applies only when the job didn't specify voice/model explicitly):

```go
	if wp.settings != nil {
		st := wp.settings.Get()
		if st.Voice != "" && job.Voice == "" {
			req.Voice = st.Voice
		}
		if st.Model != "" && job.Model == "" {
			req.Model = st.Model
		}
	}
```

(Place this BEFORE `req.Text = job.Text`, or right after it — either works since Text is independent. Keep it before the Telegram fail-fast block so the caption/synth use the overridden values.)

In the Telegram delivery branch, build a caption and pass it to `Send`. Replace the `wp.telegram.Send(...)` call so it includes a caption:

```go
		caption := fmt.Sprintf("%.2f¢ · %s · %s",
			cost.CentsFor(provider.Name(), req.Model, len(job.Text)),
			cost.EffectiveModel(provider.Name(), req.Model),
			req.Voice)
		if sendErr := wp.telegram.Send(context.Background(), tgAudio.Data, tgAudio.Format, caption); sendErr != nil {
```

(That replaces the previous `wp.telegram.Send(context.Background(), tgAudio.Data, tgAudio.Format, job.Text)` line — caption now carries cost·model·voice instead of the raw text.)

- [ ] **Step 4: Run tests**

Run: `go test ./internal/server/ -v`
Expected: PASS (new override/caption tests + existing mode tests).

- [ ] **Step 5: Commit**

```bash
git add internal/server/worker.go internal/server/worker_test.go
git commit -m "feat(server): apply voice/model selection + cost caption on Telegram"
```

---

## Task 5: CLI — apply settings overrides + caption

**Files:**
- Modify: `cmd/speak-text/main.go`

- [ ] **Step 1: Edit `runSpeak`** in `cmd/speak-text/main.go`.

After resolving `req` and setting `req.Text = text` (and the existing voice/speed overrides), apply the persisted settings the same way the worker does, and build a caption for the Telegram send. Add imports `"github.com/ybouhjira/claude-code-tts/internal/cost"` and `"github.com/ybouhjira/claude-code-tts/internal/voicemode"` and `"fmt"` (already present).

Insert, right after `req.Text = text`:

```go
	st := voicemode.DefaultSettingsStore().Get()
	if st.Voice != "" && *voice == "" {
		req.Voice = st.Voice
	}
	if st.Model != "" {
		req.Model = st.Model
	}
```

In the `dest.SendsTelegram()` block, change the `sender.Send(...)` call to pass a caption:

```go
		caption := fmt.Sprintf("%.2f¢ · %s · %s",
			cost.CentsFor(prov.Name(), req.Model, len(text)),
			cost.EffectiveModel(prov.Name(), req.Model),
			req.Voice)
		if err := sender.Send(context.Background(), tgOut.Data, tgOut.Format, caption); err != nil {
```

(Replaces `sender.Send(context.Background(), tgOut.Data, tgOut.Format, text)`.)

- [ ] **Step 2: Build + vet**

Run: `go build ./cmd/speak-text/ && go vet ./cmd/speak-text/`
Expected: clean.

- [ ] **Step 3: Commit**

```bash
git add cmd/speak-text/main.go
git commit -m "feat(cli): apply voice/model selection + cost caption on Telegram"
```

---

## Task 6: `botcontrol.Poller`

**Files:**
- Create: `internal/botcontrol/poller.go`
- Test: `internal/botcontrol/poller_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/botcontrol/poller_test.go
package botcontrol

import (
	"context"
	"testing"

	"github.com/ybouhjira/claude-code-tts/internal/telegram"
	"github.com/ybouhjira/claude-code-tts/internal/voicemode"
)

type fakeBot struct {
	sentMsgs    []string
	voiceDemos  []string
	answers     []string
}

func (f *fakeBot) GetUpdates(ctx context.Context, offset, timeout int) ([]telegram.Update, error) {
	return nil, nil
}
func (f *fakeBot) SendMessage(ctx context.Context, text string, kb [][]telegram.InlineButton) error {
	f.sentMsgs = append(f.sentMsgs, text)
	return nil
}
func (f *fakeBot) AnswerCallback(ctx context.Context, id, text string) error {
	f.answers = append(f.answers, text)
	return nil
}
func (f *fakeBot) SendVoiceWithButton(ctx context.Context, audio []byte, caption string, kb [][]telegram.InlineButton) error {
	f.voiceDemos = append(f.voiceDemos, caption)
	return nil
}

type fakeSource struct{}

func (fakeSource) Voices() []string { return []string{"alloy", "onyx"} }
func (fakeSource) Models() []string { return []string{"tts-1", "tts-1-hd"} }
func (fakeSource) Demo(ctx context.Context, voice string) ([]byte, string, error) {
	return []byte("OGG-" + voice), "opus", nil
}

func newTestPoller(t *testing.T, bot *fakeBot) (*Poller, *voicemode.SettingsStore) {
	t.Helper()
	ss := voicemode.NewSettingsStore(t.TempDir() + "/vs.json")
	return NewPoller(bot, ss, fakeSource{}, 42), ss
}

func TestPoller_VoicesCommandSendsDemos(t *testing.T) {
	bot := &fakeBot{}
	p, _ := newTestPoller(t, bot)
	p.handleUpdate(context.Background(), telegram.Update{
		Message: &telegram.Message{Text: "/voices", Chat: telegram.Chat{ID: 42}},
	})
	if len(bot.voiceDemos) != 2 {
		t.Errorf("got %d demos, want 2 (alloy, onyx)", len(bot.voiceDemos))
	}
}

func TestPoller_ModelCommandSendsMenu(t *testing.T) {
	bot := &fakeBot{}
	p, _ := newTestPoller(t, bot)
	p.handleUpdate(context.Background(), telegram.Update{
		Message: &telegram.Message{Text: "/model", Chat: telegram.Chat{ID: 42}},
	})
	if len(bot.sentMsgs) != 1 {
		t.Errorf("got %d messages, want 1 model menu", len(bot.sentMsgs))
	}
}

func TestPoller_CallbackSetsVoice(t *testing.T) {
	bot := &fakeBot{}
	p, ss := newTestPoller(t, bot)
	p.handleUpdate(context.Background(), telegram.Update{
		CallbackQuery: &telegram.CallbackQuery{ID: "cb1", Data: "voice:onyx", Message: &telegram.Message{Chat: telegram.Chat{ID: 42}}},
	})
	if ss.Get().Voice != "onyx" {
		t.Errorf("voice = %q, want onyx", ss.Get().Voice)
	}
	if len(bot.answers) != 1 {
		t.Errorf("expected an answerCallback")
	}
}

func TestPoller_IgnoresWrongChat(t *testing.T) {
	bot := &fakeBot{}
	p, _ := newTestPoller(t, bot)
	p.handleUpdate(context.Background(), telegram.Update{
		Message: &telegram.Message{Text: "/voices", Chat: telegram.Chat{ID: 999}},
	})
	if len(bot.voiceDemos) != 0 || len(bot.sentMsgs) != 0 {
		t.Errorf("update from wrong chat should be ignored")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/botcontrol/ -v`
Expected: FAIL — package/`NewPoller` undefined.

- [ ] **Step 3: Write the implementation**

```go
// internal/botcontrol/poller.go
package botcontrol

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ybouhjira/claude-code-tts/internal/logging"
	"github.com/ybouhjira/claude-code-tts/internal/telegram"
	"github.com/ybouhjira/claude-code-tts/internal/voicemode"
)

// botSender is the Telegram surface the poller needs (satisfied by *telegram.Sender).
type botSender interface {
	GetUpdates(ctx context.Context, offset, timeout int) ([]telegram.Update, error)
	SendMessage(ctx context.Context, text string, keyboard [][]telegram.InlineButton) error
	AnswerCallback(ctx context.Context, callbackID, text string) error
	SendVoiceWithButton(ctx context.Context, audio []byte, caption string, keyboard [][]telegram.InlineButton) error
}

// settingsWriter persists the user's selection (satisfied by *voicemode.SettingsStore).
type settingsWriter interface {
	Get() voicemode.Settings
	SetVoice(string) error
	SetModel(string) error
}

// voiceModelSource exposes the current provider's voices/models and synthesizes
// demo clips.
type voiceModelSource interface {
	Voices() []string
	Models() []string
	Demo(ctx context.Context, voice string) (audio []byte, format string, err error)
}

// Poller turns Telegram messages/button-taps into voice/model selection changes.
type Poller struct {
	bot      botSender
	settings settingsWriter
	src      voiceModelSource
	chatID   int64
}

// NewPoller creates a Poller restricted to a single chat id.
func NewPoller(bot botSender, settings settingsWriter, src voiceModelSource, chatID int64) *Poller {
	return &Poller{bot: bot, settings: settings, src: src, chatID: chatID}
}

// Run long-polls Telegram until ctx is cancelled. It never returns an error;
// transient failures are logged and retried.
func (p *Poller) Run(ctx context.Context) {
	offset := 0
	for {
		if ctx.Err() != nil {
			return
		}
		// Poll timeout (20s) MUST stay under the Sender's 30s HTTP client timeout,
		// or the client aborts the request right as an idle long-poll returns.
		updates, err := p.bot.GetUpdates(ctx, offset, 20)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			logging.Error("botcontrol: getUpdates: %v", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
			}
			continue
		}
		for _, u := range updates {
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}
			p.handleUpdate(ctx, u)
		}
	}
}

// handleUpdate processes a single update (commands and button taps). It ignores
// anything not from the configured chat id.
func (p *Poller) handleUpdate(ctx context.Context, u telegram.Update) {
	switch {
	case u.Message != nil:
		if u.Message.Chat.ID != p.chatID {
			return
		}
		p.handleCommand(ctx, strings.TrimSpace(u.Message.Text))
	case u.CallbackQuery != nil:
		if u.CallbackQuery.Message == nil || u.CallbackQuery.Message.Chat.ID != p.chatID {
			return
		}
		p.handleCallback(ctx, u.CallbackQuery)
	}
}

func (p *Poller) handleCommand(ctx context.Context, text string) {
	cmd := strings.Fields(text)
	if len(cmd) == 0 {
		return
	}
	switch cmd[0] {
	case "/voices":
		for _, v := range p.src.Voices() {
			audio, format, err := p.src.Demo(ctx, v)
			if err != nil {
				logging.Error("botcontrol: demo %q: %v", v, err)
				continue
			}
			kb := [][]telegram.InlineButton{{{Text: "✅ Use " + v, CallbackData: "voice:" + v}}}
			if err := p.bot.SendVoiceWithButton(ctx, audio, "🔊 "+v, kb); err != nil {
				logging.Error("botcontrol: send demo %q: %v", v, err)
			}
		}
	case "/model":
		p.bot.SendMessage(ctx, "Pick a model:", modelKeyboard(p.src.Models()))
	case "/menu":
		st := p.settings.Get()
		p.bot.SendMessage(ctx, fmt.Sprintf("Current voice: %s\nCurrent model: %s",
			orDefault(st.Voice, "(profile default)"), orDefault(st.Model, "(profile default)")),
			modelKeyboard(p.src.Models()))
	case "/help", "/start":
		p.bot.SendMessage(ctx, "Commands:\n/voices — hear each voice and tap to use it\n/model — pick a model\n/menu — show current selection", nil)
	default:
		p.bot.SendMessage(ctx, "Unknown command. Try /voices, /model, /menu, or /help.", nil)
	}
}

func (p *Poller) handleCallback(ctx context.Context, cq *telegram.CallbackQuery) {
	parts := strings.SplitN(cq.Data, ":", 2)
	if len(parts) != 2 {
		p.bot.AnswerCallback(ctx, cq.ID, "unrecognized")
		return
	}
	kind, val := parts[0], parts[1]
	var err error
	var msg string
	switch kind {
	case "voice":
		err = p.settings.SetVoice(val)
		msg = "Voice set to " + val
	case "model":
		err = p.settings.SetModel(val)
		msg = "Model set to " + val
	default:
		p.bot.AnswerCallback(ctx, cq.ID, "unrecognized")
		return
	}
	if err != nil {
		logging.Error("botcontrol: save selection: %v", err)
		p.bot.AnswerCallback(ctx, cq.ID, "couldn't save")
		return
	}
	p.bot.AnswerCallback(ctx, cq.ID, msg)
}

func modelKeyboard(models []string) [][]telegram.InlineButton {
	kb := make([][]telegram.InlineButton, 0, len(models))
	for _, m := range models {
		kb = append(kb, []telegram.InlineButton{{Text: m, CallbackData: "model:" + m}})
	}
	return kb
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/botcontrol/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/botcontrol/
git commit -m "feat(botcontrol): command + callback poller for voice/model selection"
```

---

## Task 7: Wire the settings store + poller into the server

**Files:**
- Modify: `internal/server/server.go`

- [ ] **Step 1: Edit `server.go`**

Add imports: `"context"` (likely present), `"strconv"`, `"github.com/ybouhjira/claude-code-tts/internal/botcontrol"`, `"github.com/ybouhjira/claude-code-tts/internal/cost"`, `"github.com/ybouhjira/claude-code-tts/internal/tts"`, `"github.com/ybouhjira/claude-code-tts/internal/voicemode"` (some already present).

Add fields to `Server` for poller lifecycle:

```go
type Server struct {
	mcpServer  *server.MCPServer
	workerPool *WorkerPool
	modeStore  *voicemode.Store
	pollerStop context.CancelFunc // nil when no poller
}
```

In `New()`, after building `reg`, `modeStore`, `tgSender`/`tgReason` and the worker pool, also build the settings store, attach it to the pool, and (when Telegram is configured) start the poller. Replace the worker-pool construction + add the poller block:

```go
	settingsStore := voicemode.DefaultSettingsStore()

	wp := NewWorkerPool(reg, player, 2, 50).
		WithMode(modeStore).
		WithSettings(settingsStore)
	if tgSender != nil {
		wp.WithTelegram(tgSender, "")
	} else {
		wp.WithTelegram(nil, tgReason)
	}
	wp.Start()

	s := &Server{mcpServer: nil, workerPool: wp, modeStore: modeStore}

	// Start the Telegram control poller when configured + the chat id parses.
	if tgSender != nil {
		if chatID, err := strconv.ParseInt(reg.TelegramChatID(), 10, 64); err == nil && chatID != 0 {
			ctx, cancel := context.WithCancel(context.Background())
			s.pollerStop = cancel
			poller := botcontrol.NewPoller(tgSender, settingsStore, &registrySource{reg: reg}, chatID)
			go poller.Run(ctx)
			logging.Info("Telegram control poller started")
		} else {
			logging.Info("Telegram poller not started: chat_id missing/invalid")
		}
	}

	mcpSrv := server.NewMCPServer("claude-code-tts", "1.0.0", server.WithToolCapabilities(true))
	s.mcpServer = mcpSrv
	s.registerTools()
	return s, nil
```

(Adjust to your file's exact current `New()`; the key changes are: build `settingsStore`, `.WithSettings(settingsStore)`, and the poller block. Keep the existing tts_output/registerTools wiring.)

In `Shutdown()`, stop the poller before stopping the worker pool:

```go
func (s *Server) Shutdown() {
	if s.pollerStop != nil {
		s.pollerStop()
	}
	s.workerPool.Stop()
}
```

(Preserve any existing logging in `Shutdown`.)

Add the registry-backed source at the bottom of `server.go`:

```go
// registrySource adapts the ttsconfig registry to botcontrol.voiceModelSource:
// it reports the current provider's voices/models and synthesizes demo clips.
type registrySource struct{ reg *ttsconfig.Registry }

func (s *registrySource) Voices() []string {
	prov, _, err := s.reg.Default()
	if err != nil {
		return nil
	}
	return prov.Voices()
}

func (s *registrySource) Models() []string {
	prov, _, err := s.reg.Default()
	if err != nil {
		return nil
	}
	return cost.ModelsFor(prov.Name())
}

func (s *registrySource) Demo(ctx context.Context, voice string) ([]byte, string, error) {
	prov, req, err := s.reg.Default()
	if err != nil {
		return nil, "", err
	}
	req.Voice = voice
	req.Text = "Hi, this is the " + voice + " voice."
	req.Format = "opus"
	a, err := prov.Synthesize(ctx, req)
	if err != nil {
		return nil, "", err
	}
	return a.Data, a.Format, nil
}
```

- [ ] **Step 2: Add a `TelegramChatID` accessor** on the registry in `internal/ttsconfig/registry.go` (the poller needs the configured chat id):

```go
// TelegramChatID returns the configured Telegram chat id ("" when unset).
func (r *Registry) TelegramChatID() string {
	if r.cfg.Telegram == nil {
		return ""
	}
	return r.cfg.Telegram.ChatID
}
```

- [ ] **Step 3: Verify**

Run:
```
go vet ./internal/server/ ./internal/ttsconfig/
go test ./internal/server/ ./internal/ttsconfig/
go build ./cmd/tts-server/
```
Expected: vet clean; tests pass; tts-server builds.

- [ ] **Step 4: Commit**

```bash
git add internal/server/server.go internal/ttsconfig/registry.go
git commit -m "feat(server): start Telegram control poller; expose chat id"
```

---

## Task 8: Docs + full build/test

**Files:**
- Modify: `README.md`, `CLAUDE.md`

- [ ] **Step 1: Document in `README.md`**

Add to the "Voice output & Telegram" section: the bot commands (`/voices` — demos + tap to use; `/model` — pick a model; `/menu`; `/help`); that the bot only listens while Claude Code is open and only obeys the configured `chat_id`; that each voice message shows an estimated `cost¢ · model · voice` caption; and that the selected voice/model persist in `voice-settings.json`.

- [ ] **Step 2: Update `CLAUDE.md`**

Add the `internal/cost` and `internal/botcontrol` packages and the `voicemode.SettingsStore` to the architecture section; note the Telegram poller runs in `tts-server`.

- [ ] **Step 3: Format, build, vet, full test**

Run:
```
gofmt -w .
go build ./...
go vet ./...
go test ./...
```
Expected: all build and pass (`go test ./...` ~30–60s; the audio package runs the real player but should not hang). Fix any leftover compile issues the compiler surfaces.

- [ ] **Step 4: Commit**

```bash
git add README.md CLAUDE.md
git commit -m "docs: document interactive Telegram bot control + cost caption"
```

---

## Self-Review Notes (for the implementer)

- **Spec coverage:** cost estimate (Task 1, §5.4); settings store (Task 2, §5.2); telegram getUpdates/answerCallback/sendMessage/inline-keyboards (Task 3, §5.1); worker overrides + caption (Task 4, §5.3/§5.5); CLI parity (Task 5); poller commands/callbacks + chat-id authorization (Task 6, §5.6/§5.7/§7); server wiring + start/stop + registry source (Task 7); docs (Task 8). Security: chat-id check in `handleUpdate` (Task 6) + only starting the poller when configured (Task 7).
- **Type consistency:** `cost.CentsFor/EffectiveModel/ModelsFor`; `voicemode.Settings{Voice,Model}` + `SettingsStore` (`Get`/`SetVoice`/`SetModel`); telegram `Update`/`Message`/`Chat`/`CallbackQuery`/`InlineButton` + `GetUpdates`/`SendMessage`/`AnswerCallback`/`SendVoiceWithButton` + `sendFile(...keyboard)`; worker `settingsReader` + `WithSettings`; botcontrol `botSender`/`settingsWriter`/`voiceModelSource` interfaces (the real `*telegram.Sender`, `*voicemode.SettingsStore`, and `*registrySource` satisfy them); `registrySource` uses `reg.Default()` + `cost.ModelsFor`. `reg.TelegramChatID()` added in Task 7.
- **Nil-safety / coupling:** `WithSettings` is nil-safe so existing `NewWorkerPool` callers/tests are unchanged; Task 4 (worker) and Task 7 (server) compile independently. The poller only starts when a Telegram sender exists and the chat id parses.
- **Out of scope (per spec):** always-on daemon, provider switching from the bot, webhooks, multi-user, exact billing.
