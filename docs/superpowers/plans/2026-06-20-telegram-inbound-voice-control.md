# Telegram → Claude Two-Way Voice Control — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let the user reply to Claude from Telegram (text or voice note) and have the reply injected into their live terminal `claude` session, with voice notes transcribed and translated to English.

**Architecture:** A new `claude-tts run` command launches the real `claude` inside a pseudo-terminal it owns (so it can inject input reliably even when the PC is locked). A background poller reads Telegram `getUpdates`, filters to the configured `chat_id`, transcribes+translates voice notes via OpenAI, and injects the text into the PTY. The outbound audio path is unchanged.

**Tech Stack:** Go 1.23, `github.com/aymanbagabas/go-pty` (cross-platform PTY incl. Windows ConPTY), `golang.org/x/term` (raw mode), OpenAI HTTP APIs (`/v1/audio/transcriptions`, `/v1/chat/completions`), Telegram Bot API. Module path: `github.com/ybouhjira/claude-code-tts`.

## Global Constraints

- Go version floor: **1.23** (per `go.mod`); do not raise it.
- Module path is `github.com/ybouhjira/claude-code-tts`; all internal imports use that prefix.
- API keys come **only** from environment variables, never config or logs. The Telegram bot token must be redacted from every error/log (reuse the existing `redact` pattern in `internal/telegram/telegram.go`).
- Inbound messages are accepted **only** from the configured `chat_id`. Everything else is dropped and logged.
- STT/translate reuse `OPENAI_API_KEY`.
- New code must pass `go build ./...`, `go vet ./...`, `gofmt -l` (clean), `golangci-lint run ./...` (0 issues), and `go test ./...`. Run network-free, hermetic tests (mock HTTP via `httptest`; never call real Telegram/OpenAI/`claude`).
- Do not modify the outbound audio path (auto-speak hook, relay, `telegram.Sender`).
- Windows note: run all `go`/`gofmt` via `export PATH="$PATH:/c/Users/leofu/scoop/shims"` in bash.

## File Structure

- `internal/ttsconfig/config.go` (modify) — add `InboundConfig` + `TelegramConfig.Inbound` + `ResolvedInbound()`.
- `internal/stt/stt.go` (create) — OpenAI transcribe + translate client.
- `internal/stt/stt_test.go` (create).
- `internal/telegram/receiver.go` (create) — `Receiver`: `GetUpdates`, `DownloadVoice`, update types.
- `internal/telegram/receiver_test.go` (create).
- `internal/inbound/poller.go` (create) — `Poller` (routing/filter/STT/translate/inject) + single-flight lockfile. Testable core.
- `internal/inbound/poller_test.go` (create).
- `internal/ptybridge/bridge.go` (create) — `Bridge`: spawn `claude` in a PTY, proxy I/O, `Inject(text)`, direct-exec fallback.
- `internal/ptybridge/bridge_test.go` (create) — injection-encoding unit test (fake writer).
- `cmd/claude-run/main.go` (create) — wires config + bridge + receiver + stt + poller.
- `config.example.json` (modify) — add a commented `telegram.inbound` block.
- `docs/telegram-inbound.md` (create) — setup (alias `claude`), config, security.
- `go.mod` / `go.sum` (modify) — add `go-pty`, `golang.org/x/term`.

---

### Task 1: Inbound config (`ttsconfig`)

**Files:**
- Modify: `internal/ttsconfig/config.go`
- Test: `internal/ttsconfig/config_inbound_test.go`

**Interfaces:**
- Produces: `type InboundConfig struct{...}`; field `TelegramConfig.Inbound *InboundConfig`; method `func (t *TelegramConfig) ResolvedInbound() ResolvedInbound`; `type ResolvedInbound struct{ Enabled bool; TranscribeModel string; Translate bool; SourceLanguage string; TargetLanguage string; RequireReply bool }`.

- [ ] **Step 1: Write the failing test**

```go
// internal/ttsconfig/config_inbound_test.go
package ttsconfig

import "testing"

func TestResolvedInbound_Defaults(t *testing.T) {
	// Inbound block present but mostly empty: defaults fill in.
	tc := &TelegramConfig{Inbound: &InboundConfig{Enabled: true}}
	got := tc.ResolvedInbound()
	if !got.Enabled {
		t.Fatal("Enabled should be true")
	}
	if got.TranscribeModel != "gpt-4o-mini-transcribe" {
		t.Errorf("TranscribeModel default = %q, want gpt-4o-mini-transcribe", got.TranscribeModel)
	}
	if !got.Translate {
		t.Error("Translate should default to true when omitted")
	}
	if got.SourceLanguage != "auto" || got.TargetLanguage != "English" {
		t.Errorf("language defaults = %q/%q, want auto/English", got.SourceLanguage, got.TargetLanguage)
	}
}

func TestResolvedInbound_TranslateExplicitFalse(t *testing.T) {
	no := false
	tc := &TelegramConfig{Inbound: &InboundConfig{Enabled: true, Translate: &no}}
	if tc.ResolvedInbound().Translate {
		t.Error("Translate should honor explicit false")
	}
}

func TestResolvedInbound_NilTelegram(t *testing.T) {
	var tc *TelegramConfig
	if tc.ResolvedInbound().Enabled {
		t.Error("nil telegram config should yield disabled inbound")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `export PATH="$PATH:/c/Users/leofu/scoop/shims"; go test ./internal/ttsconfig/ -run ResolvedInbound -v`
Expected: FAIL — `InboundConfig`/`ResolvedInbound` undefined (compile error).

- [ ] **Step 3: Add the types and method to `config.go`**

Add after the `TelegramConfig` struct (line ~32) and add the `Inbound` field to `TelegramConfig`:

```go
// TelegramConfig configures Telegram delivery. The bot token is read from the
// named environment variable; the chat id is stored directly (not secret).
type TelegramConfig struct {
	BotTokenEnv string         `json:"bot_token_env"`
	ChatID      string         `json:"chat_id"`
	Inbound     *InboundConfig `json:"inbound,omitempty"`
}

// InboundConfig configures the Telegram→Claude return path. Translate is a
// pointer so an omitted value can default to true (on) rather than false.
type InboundConfig struct {
	Enabled         bool   `json:"enabled"`
	TranscribeModel string `json:"transcribe_model"`
	Translate       *bool  `json:"translate"`
	SourceLanguage  string `json:"source_language"`
	TargetLanguage  string `json:"target_language"`
	RequireReply    bool   `json:"require_reply"`
}

// ResolvedInbound is InboundConfig with all defaults applied.
type ResolvedInbound struct {
	Enabled         bool
	TranscribeModel string
	Translate       bool
	SourceLanguage  string
	TargetLanguage  string
	RequireReply    bool
}

// ResolvedInbound applies defaults: transcribe model gpt-4o-mini-transcribe,
// translate on, source auto, target English. A nil TelegramConfig or nil
// Inbound yields a disabled result.
func (t *TelegramConfig) ResolvedInbound() ResolvedInbound {
	in := InboundConfig{}
	if t != nil && t.Inbound != nil {
		in = *t.Inbound
	}
	r := ResolvedInbound{
		Enabled:         in.Enabled,
		TranscribeModel: in.TranscribeModel,
		SourceLanguage:  in.SourceLanguage,
		TargetLanguage:  in.TargetLanguage,
		RequireReply:    in.RequireReply,
		Translate:       true,
	}
	if in.Translate != nil {
		r.Translate = *in.Translate
	}
	if r.TranscribeModel == "" {
		r.TranscribeModel = "gpt-4o-mini-transcribe"
	}
	if r.SourceLanguage == "" {
		r.SourceLanguage = "auto"
	}
	if r.TargetLanguage == "" {
		r.TargetLanguage = "English"
	}
	return r
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/ttsconfig/ -run ResolvedInbound -v`
Expected: PASS (3 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/ttsconfig/config.go internal/ttsconfig/config_inbound_test.go
git commit -m "feat(config): add telegram.inbound config with defaults"
```

---

### Task 2: STT client (`internal/stt`)

**Files:**
- Create: `internal/stt/stt.go`
- Test: `internal/stt/stt_test.go`

**Interfaces:**
- Produces: `func New(apiKey string) *Client`; `func (c *Client) WithBaseURL(u string) *Client`; `func (c *Client) Transcribe(ctx context.Context, ogg []byte, model, language string) (string, error)`; `func (c *Client) Translate(ctx context.Context, text, targetLanguage string) (string, error)`.

- [ ] **Step 1: Write the failing test**

```go
// internal/stt/stt_test.go
package stt

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTranscribe_PostsMultipartAndReturnsText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/transcriptions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			t.Errorf("content-type = %s", r.Header.Get("Content-Type"))
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("auth = %s", r.Header.Get("Authorization"))
		}
		_ = r.ParseMultipartForm(1 << 20)
		if r.FormValue("model") != "gpt-4o-mini-transcribe" {
			t.Errorf("model = %s", r.FormValue("model"))
		}
		io.WriteString(w, "shalom world")
	}))
	defer srv.Close()

	c := New("test-key").WithBaseURL(srv.URL)
	got, err := c.Transcribe(context.Background(), []byte("OggS-fake"), "gpt-4o-mini-transcribe", "he")
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if got != "shalom world" {
		t.Errorf("got %q", got)
	}
}

func TestTranslate_PostsChatAndReturnsContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"hello world"}}]}`)
	}))
	defer srv.Close()

	c := New("test-key").WithBaseURL(srv.URL)
	got, err := c.Translate(context.Background(), "shalom world", "English")
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if got != "hello world" {
		t.Errorf("got %q", got)
	}
}

func TestTranscribe_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, "bad key")
	}))
	defer srv.Close()
	c := New("test-key").WithBaseURL(srv.URL)
	if _, err := c.Transcribe(context.Background(), []byte("x"), "m", ""); err == nil {
		t.Fatal("expected error on 401")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/stt/ -v`
Expected: FAIL — package/functions undefined.

- [ ] **Step 3: Write the implementation**

```go
// internal/stt/stt.go
// Package stt wraps the OpenAI speech-to-text and translation HTTP APIs.
package stt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"time"
)

// Client calls the OpenAI audio transcription and chat-completion endpoints.
type Client struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
}

// New creates a Client using the given API key (from OPENAI_API_KEY).
func New(apiKey string) *Client {
	return &Client{
		apiKey:     apiKey,
		baseURL:    "https://api.openai.com",
		httpClient: &http.Client{Timeout: 60 * time.Second},
	}
}

// WithBaseURL overrides the API base URL (used by tests).
func (c *Client) WithBaseURL(u string) *Client { c.baseURL = u; return c }

// Transcribe converts OGG/Opus audio (as delivered by Telegram voice notes) to
// text via /v1/audio/transcriptions. language is an optional ISO-639-1 hint
// ("" or "auto" omits it).
func (c *Client) Transcribe(ctx context.Context, ogg []byte, model, language string) (string, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	_ = w.WriteField("model", model)
	_ = w.WriteField("response_format", "text")
	if language != "" && language != "auto" {
		_ = w.WriteField("language", language)
	}
	part, err := w.CreateFormFile("file", "voice.ogg")
	if err != nil {
		return "", fmt.Errorf("stt: form file: %w", err)
	}
	if _, err := part.Write(ogg); err != nil {
		return "", fmt.Errorf("stt: write audio: %w", err)
	}
	if err := w.Close(); err != nil {
		return "", fmt.Errorf("stt: close writer: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/audio/transcriptions", &body)
	if err != nil {
		return "", fmt.Errorf("stt: request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("stt: transcribe request: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("stt: transcribe status %d: %s", resp.StatusCode, string(data))
	}
	return string(bytes.TrimSpace(data)), nil
}

// Translate translates text into targetLanguage using a small chat model.
func (c *Client) Translate(ctx context.Context, text, targetLanguage string) (string, error) {
	reqBody, _ := json.Marshal(map[string]any{
		"model": "gpt-4o-mini",
		"messages": []map[string]string{
			{"role": "system", "content": fmt.Sprintf(
				"Translate the user's message into %s. Output only the translation, no quotes or commentary.", targetLanguage)},
			{"role": "user", "content": text},
		},
		"temperature": 0,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		return "", fmt.Errorf("stt: translate request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("stt: translate request: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("stt: translate status %d: %s", resp.StatusCode, string(data))
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return "", fmt.Errorf("stt: parse translate response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("stt: translate returned no choices")
	}
	return parsed.Choices[0].Message.Content, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/stt/ -v`
Expected: PASS (3 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/stt/
git commit -m "feat(stt): OpenAI transcribe + translate client"
```

---

### Task 3: Telegram receiver (`internal/telegram`)

**Files:**
- Create: `internal/telegram/receiver.go`
- Test: `internal/telegram/receiver_test.go`

**Interfaces:**
- Produces: `type Update struct{ UpdateID int; Message *Message }`; `type Message struct{ Chat Chat; Text string; Voice *Voice; ReplyToMessage *Message }`; `type Chat struct{ ID int64 }`; `type Voice struct{ FileID string }`; `func NewReceiver(token string) *Receiver`; `func (r *Receiver) WithBaseURL(u string) *Receiver`; `func (r *Receiver) GetUpdates(ctx context.Context, offset, timeoutSec int) ([]Update, error)`; `func (r *Receiver) DownloadVoice(ctx context.Context, fileID string) ([]byte, error)`.

- [ ] **Step 1: Write the failing test**

```go
// internal/telegram/receiver_test.go
package telegram

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGetUpdates_ParsesMessages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/getUpdates") {
			t.Errorf("path = %s", r.URL.Path)
		}
		io.WriteString(w, `{"ok":true,"result":[
			{"update_id":10,"message":{"chat":{"id":42},"text":"hello"}},
			{"update_id":11,"message":{"chat":{"id":42},"voice":{"file_id":"AwAC123"}}}
		]}`)
	}))
	defer srv.Close()

	r := NewReceiver("tok").WithBaseURL(srv.URL)
	ups, err := r.GetUpdates(context.Background(), 0, 0)
	if err != nil {
		t.Fatalf("GetUpdates: %v", err)
	}
	if len(ups) != 2 {
		t.Fatalf("len = %d, want 2", len(ups))
	}
	if ups[0].Message.Text != "hello" || ups[0].Message.Chat.ID != 42 {
		t.Errorf("msg0 = %+v", ups[0].Message)
	}
	if ups[1].Message.Voice == nil || ups[1].Message.Voice.FileID != "AwAC123" {
		t.Errorf("msg1 voice = %+v", ups[1].Message.Voice)
	}
}

func TestDownloadVoice_GetFileThenDownload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/getFile"):
			io.WriteString(w, `{"ok":true,"result":{"file_path":"voice/file_1.oga"}}`)
		case strings.Contains(r.URL.Path, "/file/bot"):
			io.WriteString(w, "OGG-BYTES")
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	r := NewReceiver("tok").WithBaseURL(srv.URL)
	data, err := r.DownloadVoice(context.Background(), "AwAC123")
	if err != nil {
		t.Fatalf("DownloadVoice: %v", err)
	}
	if string(data) != "OGG-BYTES" {
		t.Errorf("data = %q", string(data))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/telegram/ -run 'GetUpdates|DownloadVoice' -v`
Expected: FAIL — `Receiver`/types undefined.

- [ ] **Step 3: Write the implementation**

```go
// internal/telegram/receiver.go
package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Update is a single Telegram update (we only care about messages).
type Update struct {
	UpdateID int      `json:"update_id"`
	Message  *Message `json:"message"`
}

// Message is a Telegram message (subset).
type Message struct {
	Chat           Chat     `json:"chat"`
	Text           string   `json:"text"`
	Voice          *Voice   `json:"voice"`
	ReplyToMessage *Message `json:"reply_to_message"`
}

// Chat identifies the chat a message belongs to.
type Chat struct {
	ID int64 `json:"id"`
}

// Voice is a Telegram voice note (OGG/Opus).
type Voice struct {
	FileID string `json:"file_id"`
}

// Receiver reads inbound updates from the Telegram Bot API.
type Receiver struct {
	token      string
	baseURL    string
	httpClient *http.Client
}

// NewReceiver creates a Receiver for the given bot token.
func NewReceiver(token string) *Receiver {
	return &Receiver{
		token:      token,
		baseURL:    "https://api.telegram.org",
		httpClient: &http.Client{Timeout: 70 * time.Second}, // > long-poll timeout
	}
}

// WithBaseURL overrides the API base (used by tests).
func (r *Receiver) WithBaseURL(u string) *Receiver { r.baseURL = u; return r }

func (r *Receiver) redact(msg string) string {
	if r.token == "" {
		return msg
	}
	return strings.ReplaceAll(msg, r.token, "REDACTED")
}

// GetUpdates long-polls for new updates. offset acknowledges updates up to
// offset-1; timeoutSec is the server-side long-poll timeout in seconds.
func (r *Receiver) GetUpdates(ctx context.Context, offset, timeoutSec int) ([]Update, error) {
	q := url.Values{}
	q.Set("offset", strconv.Itoa(offset))
	q.Set("timeout", strconv.Itoa(timeoutSec))
	q.Set("allowed_updates", `["message"]`)
	endpoint := fmt.Sprintf("%s/bot%s/getUpdates?%s", r.baseURL, r.token, q.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("telegram: getUpdates request: %s", r.redact(err.Error()))
	}
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("telegram: getUpdates: %s", r.redact(err.Error()))
	}
	defer resp.Body.Close() //nolint:errcheck
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("telegram: getUpdates status %d: %s", resp.StatusCode, r.redact(string(data)))
	}
	var parsed struct {
		OK     bool     `json:"ok"`
		Result []Update `json:"result"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("telegram: parse getUpdates: %w", err)
	}
	return parsed.Result, nil
}

// DownloadVoice resolves a voice file id to its bytes (getFile + download).
func (r *Receiver) DownloadVoice(ctx context.Context, fileID string) ([]byte, error) {
	endpoint := fmt.Sprintf("%s/bot%s/getFile?file_id=%s", r.baseURL, r.token, url.QueryEscape(fileID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("telegram: getFile request: %s", r.redact(err.Error()))
	}
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("telegram: getFile: %s", r.redact(err.Error()))
	}
	defer resp.Body.Close() //nolint:errcheck
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("telegram: getFile status %d: %s", resp.StatusCode, r.redact(string(data)))
	}
	var parsed struct {
		Result struct {
			FilePath string `json:"file_path"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("telegram: parse getFile: %w", err)
	}
	if parsed.Result.FilePath == "" {
		return nil, fmt.Errorf("telegram: getFile returned empty path")
	}

	fileURL := fmt.Sprintf("%s/file/bot%s/%s", r.baseURL, r.token, parsed.Result.FilePath)
	freq, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return nil, fmt.Errorf("telegram: file request: %s", r.redact(err.Error()))
	}
	fresp, err := r.httpClient.Do(freq)
	if err != nil {
		return nil, fmt.Errorf("telegram: file download: %s", r.redact(err.Error()))
	}
	defer fresp.Body.Close() //nolint:errcheck
	if fresp.StatusCode < 200 || fresp.StatusCode >= 300 {
		return nil, fmt.Errorf("telegram: file status %d", fresp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(fresp.Body, 32<<20))
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/telegram/ -run 'GetUpdates|DownloadVoice' -v`
Expected: PASS (2 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/telegram/receiver.go internal/telegram/receiver_test.go
git commit -m "feat(telegram): inbound Receiver (getUpdates + voice download)"
```

---

### Task 4: Inbound poller + single-flight lock (`internal/inbound`)

This is the testable core that ties receiver+stt+injection together with the
`chat_id` filter and `require_reply` guard. The PTY itself is Task 5; here the
injector is an interface so this logic is fully unit-testable.

**Files:**
- Create: `internal/inbound/poller.go`
- Test: `internal/inbound/poller_test.go`

**Interfaces:**
- Consumes: `telegram.Update` (Task 3); `stt.Client` (Task 2); `ttsconfig.ResolvedInbound` (Task 1).
- Produces: `type Injector interface{ Inject(text string) error }`; `type Updater interface{ GetUpdates(ctx, offset, timeoutSec int) ([]telegram.Update, error) }`; `type VoiceDownloader interface{ DownloadVoice(ctx, fileID string) ([]byte, error) }`; `type Transcriber interface{ Transcribe(ctx, ogg []byte, model, language string) (string, error); Translate(ctx, text, target string) (string, error) }`; `func NewPoller(...) *Poller`; `func (p *Poller) handleMessage(ctx, *telegram.Message) (injected bool, err error)`; `func (p *Poller) Run(ctx) error`; `func AcquireSingleFlight(path string) (release func() error, ok bool, err error)`.

- [ ] **Step 1: Write the failing test**

```go
// internal/inbound/poller_test.go
package inbound

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/ybouhjira/claude-code-tts/internal/telegram"
	"github.com/ybouhjira/claude-code-tts/internal/ttsconfig"
)

type fakeInjector struct{ got []string }

func (f *fakeInjector) Inject(text string) error { f.got = append(f.got, text); return nil }

type fakeTranscriber struct{ transcript, translation string }

func (f fakeTranscriber) Transcribe(_ context.Context, _ []byte, _, _ string) (string, error) {
	return f.transcript, nil
}
func (f fakeTranscriber) Translate(_ context.Context, _ string, _ string) (string, error) {
	return f.translation, nil
}

type fakeDownloader struct{}

func (fakeDownloader) DownloadVoice(_ context.Context, _ string) ([]byte, error) {
	return []byte("ogg"), nil
}

func cfg() ttsconfig.ResolvedInbound {
	return ttsconfig.ResolvedInbound{Enabled: true, TranscribeModel: "m", Translate: true,
		SourceLanguage: "he", TargetLanguage: "English"}
}

func TestHandleMessage_TextFromAllowedChat_Injects(t *testing.T) {
	inj := &fakeInjector{}
	p := NewPoller(nil, fakeDownloader{}, fakeTranscriber{}, inj, 42, cfg())
	msg := &telegram.Message{Chat: telegram.Chat{ID: 42}, Text: "do the thing"}
	ok, err := p.handleMessage(context.Background(), msg)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if len(inj.got) != 1 || inj.got[0] != "do the thing" {
		t.Errorf("injected %v", inj.got)
	}
}

func TestHandleMessage_WrongChat_Dropped(t *testing.T) {
	inj := &fakeInjector{}
	p := NewPoller(nil, fakeDownloader{}, fakeTranscriber{}, inj, 42, cfg())
	msg := &telegram.Message{Chat: telegram.Chat{ID: 999}, Text: "evil"}
	ok, _ := p.handleMessage(context.Background(), msg)
	if ok || len(inj.got) != 0 {
		t.Errorf("message from wrong chat must be dropped; injected=%v", inj.got)
	}
}

func TestHandleMessage_Voice_TranscribesTranslatesInjects(t *testing.T) {
	inj := &fakeInjector{}
	p := NewPoller(nil, fakeDownloader{}, fakeTranscriber{transcript: "shalom", translation: "hello"}, inj, 42, cfg())
	msg := &telegram.Message{Chat: telegram.Chat{ID: 42}, Voice: &telegram.Voice{FileID: "f1"}}
	ok, err := p.handleMessage(context.Background(), msg)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if len(inj.got) != 1 || inj.got[0] != "hello" {
		t.Errorf("injected %v, want translated text", inj.got)
	}
}

func TestHandleMessage_RequireReply_DropsNonReply(t *testing.T) {
	inj := &fakeInjector{}
	c := cfg()
	c.RequireReply = true
	p := NewPoller(nil, fakeDownloader{}, fakeTranscriber{}, inj, 42, c)
	msg := &telegram.Message{Chat: telegram.Chat{ID: 42}, Text: "hi"} // no ReplyToMessage
	if ok, _ := p.handleMessage(context.Background(), msg); ok {
		t.Error("require_reply should drop a non-reply message")
	}
}

func TestAcquireSingleFlight_SecondCallerFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inbound.lock")
	rel, ok, err := AcquireSingleFlight(path)
	if err != nil || !ok {
		t.Fatalf("first acquire ok=%v err=%v", ok, err)
	}
	defer func() { _ = rel() }()
	if _, ok2, _ := AcquireSingleFlight(path); ok2 {
		t.Error("second acquire should fail while held")
	}
	_ = errors.New("") // keep errors imported if unused above
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/inbound/ -v`
Expected: FAIL — package undefined.

- [ ] **Step 3: Write the implementation**

```go
// internal/inbound/poller.go
// Package inbound polls Telegram for replies and injects them into the live
// claude session. Only messages from the configured chat id are accepted.
package inbound

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/ybouhjira/claude-code-tts/internal/logging"
	"github.com/ybouhjira/claude-code-tts/internal/telegram"
	"github.com/ybouhjira/claude-code-tts/internal/ttsconfig"
)

// Injector writes a line of text into the live claude session.
type Injector interface {
	Inject(text string) error
}

// Updater long-polls Telegram for updates.
type Updater interface {
	GetUpdates(ctx context.Context, offset, timeoutSec int) ([]telegram.Update, error)
}

// VoiceDownloader fetches a voice note's bytes by file id.
type VoiceDownloader interface {
	DownloadVoice(ctx context.Context, fileID string) ([]byte, error)
}

// Transcriber turns audio into (optionally translated) text.
type Transcriber interface {
	Transcribe(ctx context.Context, ogg []byte, model, language string) (string, error)
	Translate(ctx context.Context, text, target string) (string, error)
}

// Poller wires the receiver, transcriber and injector together.
type Poller struct {
	updater     Updater
	downloader  VoiceDownloader
	transcriber Transcriber
	injector    Injector
	chatID      int64
	cfg         ttsconfig.ResolvedInbound
	offset      int
}

// NewPoller constructs a Poller. updater may be nil in unit tests that only
// exercise handleMessage.
func NewPoller(u Updater, d VoiceDownloader, t Transcriber, inj Injector, chatID int64, cfg ttsconfig.ResolvedInbound) *Poller {
	return &Poller{updater: u, downloader: d, transcriber: t, injector: inj, chatID: chatID, cfg: cfg}
}

// Run long-polls until ctx is cancelled. Transient errors are logged and retried
// with a short backoff; the loop never exits on a recoverable error.
func (p *Poller) Run(ctx context.Context) error {
	logging.Info("telegram inbound poller started (chat_id=%d)", p.chatID)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		ups, err := p.updater.GetUpdates(ctx, p.offset, 50)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			logging.Error("inbound getUpdates: %v", err)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
			}
			continue
		}
		for _, up := range ups {
			p.offset = up.UpdateID + 1
			if up.Message == nil {
				continue
			}
			if _, err := p.handleMessage(ctx, up.Message); err != nil {
				logging.Error("inbound handle message: %v", err)
			}
		}
	}
}

// handleMessage applies the security filter, derives text (transcribing voice
// notes), and injects it. Returns whether anything was injected.
func (p *Poller) handleMessage(ctx context.Context, msg *telegram.Message) (bool, error) {
	if msg.Chat.ID != p.chatID {
		logging.Error("inbound: dropping message from unauthorized chat %d", msg.Chat.ID)
		return false, nil
	}
	if p.cfg.RequireReply && msg.ReplyToMessage == nil {
		logging.Debug("inbound: dropping non-reply message (require_reply on)")
		return false, nil
	}

	text := strings.TrimSpace(msg.Text)
	if msg.Voice != nil {
		ogg, err := p.downloader.DownloadVoice(ctx, msg.Voice.FileID)
		if err != nil {
			return false, err
		}
		transcript, err := p.transcriber.Transcribe(ctx, ogg, p.cfg.TranscribeModel, p.cfg.SourceLanguage)
		if err != nil {
			return false, err
		}
		text = strings.TrimSpace(transcript)
		if p.cfg.Translate && text != "" {
			translated, err := p.transcriber.Translate(ctx, text, p.cfg.TargetLanguage)
			if err != nil {
				return false, err
			}
			text = strings.TrimSpace(translated)
		}
	}
	if text == "" {
		return false, nil
	}
	if err := p.injector.Inject(text); err != nil {
		return false, err
	}
	logging.Info("inbound: injected %d chars", len(text))
	return true, nil
}

// AcquireSingleFlight creates an exclusive lock file at path. ok is false (with
// nil error) when another process already holds it, so only one wrapped session
// consumes Telegram updates at a time.
func AcquireSingleFlight(path string) (release func() error, ok bool, err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return func() error {
		_ = f.Close()
		return os.Remove(path)
	}, true, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/inbound/ -v`
Expected: PASS (5 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/inbound/
git commit -m "feat(inbound): poller with chat_id filter, voice STT, single-flight lock"
```

---

### Task 5: PTY bridge (`internal/ptybridge`)

The PTY proxy is the one component that cannot be fully unit-tested; it needs a
real terminal and the real `claude`. We unit-test the injection **encoding** with
a fake writer, then verify the proxy manually. The bridge degrades to a direct
exec when a PTY cannot be created.

**Files:**
- Create: `internal/ptybridge/bridge.go`
- Test: `internal/ptybridge/bridge_test.go`
- Modify: `go.mod`, `go.sum`

**Interfaces:**
- Consumes: implements `inbound.Injector` (`Inject(text string) error`).
- Produces: `func New() *Bridge`; `func (b *Bridge) Start(name string, args []string) error`; `func (b *Bridge) Inject(text string) error`; `func (b *Bridge) Proxy(ctx context.Context) error`; `func (b *Bridge) Wait() error`; `func RunDirect(name string, args []string) error`.

- [ ] **Step 1: Add dependencies**

Run:
```bash
export PATH="$PATH:/c/Users/leofu/scoop/shims"
go get github.com/aymanbagabas/go-pty@latest
go get golang.org/x/term@latest
```
Expected: `go.mod`/`go.sum` updated; no error.

- [ ] **Step 2: Write the failing test (injection encoding)**

```go
// internal/ptybridge/bridge_test.go
package ptybridge

import (
	"bytes"
	"testing"
)

func TestInject_WritesTextAndCarriageReturn(t *testing.T) {
	var buf bytes.Buffer
	b := &Bridge{w: &buf} // inject into a fake writer instead of a real PTY
	if err := b.Inject("hello there"); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	if got := buf.String(); got != "hello there\r" {
		t.Errorf("got %q, want %q", got, "hello there\r")
	}
}

func TestInject_StripsTrailingNewlinesBeforeCR(t *testing.T) {
	var buf bytes.Buffer
	b := &Bridge{w: &buf}
	_ = b.Inject("multi\nline\n")
	if got := buf.String(); got != "multi\nline\r" {
		t.Errorf("got %q", got)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/ptybridge/ -v`
Expected: FAIL — `Bridge`/`Inject` undefined.

- [ ] **Step 4: Write the implementation**

> Note: `go-pty`'s exact API is verified by the spike in Step 6. The shape below
> matches its documented API (`pty.New()`, `pty.Command`, `Resize`, Read/Write).
> If a method name differs, adjust the three call sites in `Start`/`Proxy`.

```go
// internal/ptybridge/bridge.go
// Package ptybridge runs claude inside a pseudo-terminal so an external poller
// can inject input into the live session. It proxies the terminal transparently.
package ptybridge

import (
	"context"
	"io"
	"os"
	"strings"
	"sync"

	xpty "github.com/aymanbagabas/go-pty"
	"golang.org/x/term"
)

// Bridge owns a PTY hosting a child process and serializes writes to it so an
// injected reply never interleaves mid-line with the user's own keystrokes.
type Bridge struct {
	pty xpty.Pty
	cmd *xpty.Cmd
	mu  sync.Mutex
	w   io.Writer // the PTY (or a fake writer in tests)
}

// New creates an unstarted Bridge.
func New() *Bridge { return &Bridge{} }

// Start spawns name+args inside a new PTY. On any failure the caller should fall
// back to RunDirect.
func (b *Bridge) Start(name string, args []string) error {
	p, err := xpty.New()
	if err != nil {
		return err
	}
	c := p.Command(name, args...)
	if err := c.Start(); err != nil {
		_ = p.Close()
		return err
	}
	b.pty = p
	b.cmd = c
	b.w = p
	return nil
}

// Inject writes text followed by a carriage return into the PTY as if typed.
// Trailing newlines are trimmed so exactly one Enter is sent.
func (b *Bridge) Inject(text string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, err := io.WriteString(b.w, strings.TrimRight(text, "\r\n")+"\r")
	return err
}

// Proxy connects the user's terminal to the PTY (raw mode) and blocks until the
// child exits or ctx is cancelled.
func (b *Bridge) Proxy(ctx context.Context) error {
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err == nil {
		defer func() { _ = term.Restore(int(os.Stdin.Fd()), oldState) }()
	}
	if w, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
		_ = b.pty.Resize(w, h)
	}

	// PTY output -> our stdout.
	go func() { _, _ = io.Copy(os.Stdout, b.pty) }()
	// Our stdin -> PTY, but serialized with Inject via the mutex.
	go func() {
		bufr := make([]byte, 4096)
		for {
			n, rerr := os.Stdin.Read(bufr)
			if n > 0 {
				b.mu.Lock()
				_, werr := b.w.Write(bufr[:n])
				b.mu.Unlock()
				if werr != nil {
					return
				}
			}
			if rerr != nil {
				return
			}
		}
	}()

	done := make(chan error, 1)
	go func() { done <- b.cmd.Wait() }()
	select {
	case <-ctx.Done():
		_ = b.pty.Close()
		return ctx.Err()
	case err := <-done:
		_ = b.pty.Close()
		return err
	}
}

// Wait blocks until the child exits (used when Proxy is not driving the loop).
func (b *Bridge) Wait() error { return b.cmd.Wait() }

// RunDirect execs name+args attached directly to the current stdio. It is the
// fallback when a PTY cannot be created; inbound injection is unavailable here.
func RunDirect(name string, args []string) error {
	c := execCommand(name, args...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return c.Run()
}
```

Also create the tiny indirection so `RunDirect` stays importable without pulling
`os/exec` types into the public surface:

```go
// internal/ptybridge/exec.go
package ptybridge

import "os/exec"

func execCommand(name string, args ...string) *exec.Cmd { return exec.Command(name, args...) }
```

- [ ] **Step 5: Run unit test to verify it passes**

Run: `go test ./internal/ptybridge/ -v`
Expected: PASS (2 tests). The `Inject` tests use the `w` fake writer, so no real PTY is needed.

- [ ] **Step 6: Manual ConPTY spike (verify the proxy works on Windows)**

This validates the riskiest assumption before wiring the command. Build and run
the bridge against an interactive program and confirm both interactive use and
injection work.

Create a throwaway `cmd/ptyspike/main.go`:
```go
package main

import (
	"context"
	"os"
	"time"

	"github.com/ybouhjira/claude-code-tts/internal/ptybridge"
)

func main() {
	b := ptybridge.New()
	if err := b.Start("powershell", []string{"-NoLogo"}); err != nil {
		os.Exit(1)
	}
	go func() { time.Sleep(3 * time.Second); _ = b.Inject("echo INJECTED_OK") }()
	_ = b.Proxy(context.Background())
}
```
Run: `go run ./cmd/ptyspike` in Windows Terminal. Type normally; after ~3s
confirm `INJECTED_OK` is echoed by the shell (proves injection reached the PTY).
Then delete `cmd/ptyspike` before committing.

Expected: you can interact normally AND see `INJECTED_OK` appear. If the proxy
mis-renders or injection fails, that is the signal to swap PTY libraries
(alternatives: `github.com/photostorm/pty`, `github.com/UserExist/conpty`) before
proceeding — adjust only `Start`/`Proxy`/`Inject`.

- [ ] **Step 7: Commit**

```bash
rm -rf cmd/ptyspike
git add internal/ptybridge/ go.mod go.sum
git commit -m "feat(ptybridge): PTY spawn/proxy/inject with direct-exec fallback"
```

---

### Task 6: `claude-tts run` command + docs

Wires everything: load config, resolve token/chat/inbound, acquire the
single-flight lock, start the bridge, launch the poller, proxy the terminal.

**Files:**
- Create: `cmd/claude-run/main.go`
- Modify: `config.example.json`
- Create: `docs/telegram-inbound.md`

**Interfaces:**
- Consumes: `ttsconfig` (Task 1), `stt.New` (Task 2), `telegram.NewReceiver` (Task 3), `inbound.NewPoller`/`AcquireSingleFlight` (Task 4), `ptybridge.New`/`RunDirect` (Task 5).

- [ ] **Step 1: Write the command**

```go
// cmd/claude-run/main.go
// Command claude-run launches `claude` inside a PTY and injects Telegram replies
// into the live session. Invoke as `claude-tts run [claude args...]`; alias
// `claude` to it for a transparent experience.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/ybouhjira/claude-code-tts/internal/inbound"
	"github.com/ybouhjira/claude-code-tts/internal/ptybridge"
	"github.com/ybouhjira/claude-code-tts/internal/stt"
	"github.com/ybouhjira/claude-code-tts/internal/telegram"
	"github.com/ybouhjira/claude-code-tts/internal/ttsconfig"
)

func main() {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "run" {
		args = args[1:] // allow both `claude-run ...` and `claude-tts run ...`
	}

	bridge := ptybridge.New()
	if err := bridge.Start("claude", args); err != nil {
		// PTY unavailable: degrade to a plain claude with no inbound.
		fmt.Fprintf(os.Stderr, "claude-tts: PTY unavailable (%v); running claude directly without Telegram inbound\n", err)
		if rerr := ptybridge.RunDirect("claude", args); rerr != nil {
			os.Exit(1)
		}
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if release := startInbound(ctx, bridge); release != nil {
		defer func() { _ = release() }()
	}

	if err := bridge.Proxy(ctx); err != nil && ctx.Err() == nil {
		fmt.Fprintf(os.Stderr, "claude-tts: %v\n", err)
		os.Exit(1)
	}
}

// startInbound launches the Telegram poller if inbound is enabled, the bot token
// and chat id resolve, and this process wins the single-flight lock. It returns a
// release func for the lock (nil when inbound did not start).
func startInbound(ctx context.Context, bridge *ptybridge.Bridge) func() error {
	cfg, err := ttsconfig.LoadOrDefault() // see Step 2: exported loader
	if err != nil {
		return nil
	}
	in := cfg.Telegram.ResolvedInbound()
	if !in.Enabled || cfg.Telegram == nil {
		return nil
	}
	token := os.Getenv(envOr(cfg.Telegram.BotTokenEnv, "TELEGRAM_BOT_TOKEN"))
	if token == "" || cfg.Telegram.ChatID == "" {
		fmt.Fprintln(os.Stderr, "claude-tts: telegram inbound enabled but token/chat_id missing; skipping")
		return nil
	}
	chatID, err := strconv.ParseInt(cfg.Telegram.ChatID, 10, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "claude-tts: invalid chat_id %q\n", cfg.Telegram.ChatID)
		return nil
	}
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "claude-tts: OPENAI_API_KEY unset; voice transcription unavailable")
	}

	lockPath := filepath.Join(stateDir(), "inbound.lock")
	release, ok, lerr := inbound.AcquireSingleFlight(lockPath)
	if lerr != nil || !ok {
		fmt.Fprintln(os.Stderr, "claude-tts: another session owns Telegram inbound; running proxy-only")
		return nil
	}

	recv := telegram.NewReceiver(token)
	sttClient := stt.New(apiKey)
	poller := inbound.NewPoller(recv, recv, sttClient, bridge, chatID, in)
	go func() { _ = poller.Run(ctx) }()
	return release
}

func envOr(name, fallback string) string {
	if name != "" {
		return name
	}
	return fallback
}

func stateDir() string {
	if p := os.Getenv("CLAUDE_TTS_STATE"); p != "" {
		return filepath.Dir(p)
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "plugins", "claude-code-tts")
}
```

> `stt.Client` satisfies `inbound.Transcriber` (it has `Transcribe` and
> `Translate`); `telegram.Receiver` satisfies both `inbound.Updater` and
> `inbound.VoiceDownloader`; `ptybridge.Bridge` satisfies `inbound.Injector`.

- [ ] **Step 2: Export a config loader (`ttsconfig`)**

The command needs the raw `*Config`. Add to `internal/ttsconfig/config.go`:

```go
// LoadOrDefault reads the config file, returning DefaultConfig() when absent.
func LoadOrDefault() (*Config, error) { return loadConfig() }
```

- [ ] **Step 3: Build the command**

Run: `export PATH="$PATH:/c/Users/leofu/scoop/shims"; go build ./cmd/claude-run`
Expected: builds with no error.

- [ ] **Step 4: Add the Makefile target**

In `Makefile`, extend the build target to also produce `bin/claude-run`:
```make
	go build -ldflags="-s -w" -o bin/claude-run ./cmd/claude-run
```
Run: `make build` (or the bash equivalent) and confirm `bin/claude-run` exists.

- [ ] **Step 5: Document setup and update the example config**

Append a commented inbound block to `config.example.json`'s telegram section:
```json
"telegram": {
  "bot_token_env": "TELEGRAM_BOT_TOKEN",
  "chat_id": "YOUR_CHAT_ID",
  "inbound": { "enabled": true, "translate": true, "target_language": "English", "require_reply": false }
}
```

Create `docs/telegram-inbound.md` covering: aliasing `claude` →
`claude-tts run` (e.g. PowerShell `function claude { claude-tts run @args }`),
the `telegram.inbound` config reference, the **security warning** (anyone who can
message the bot can drive Claude — keep the token secret, set `chat_id`
correctly, consider `require_reply: true`), and the single-active-session note.

- [ ] **Step 6: Full verification**

Run:
```bash
export PATH="$PATH:/c/Users/leofu/scoop/shims"
go build ./... && go vet ./... && gofmt -l . && golangci-lint run ./... && go test ./...
```
Expected: build/vet exit 0; `gofmt -l` empty; lint 0 issues; all tests pass.

- [ ] **Step 7: Manual end-to-end check**

With `TELEGRAM_BOT_TOKEN`/`OPENAI_API_KEY` set and `chat_id` configured: run
`bin/claude-run` in Windows Terminal, use Claude normally, then from your phone
send (a) a text reply and (b) a voice note — confirm each appears as your next
turn in the session. Confirm a message from a different Telegram account is
ignored.

- [ ] **Step 8: Commit**

```bash
git add cmd/claude-run/ internal/ttsconfig/config.go config.example.json docs/telegram-inbound.md Makefile
git commit -m "feat(cmd): claude-tts run wrapper wiring Telegram inbound to the live session"
```

---

## Self-Review

**Spec coverage:**
- Wrapper / PTY inject → Task 5 + Task 6. ✓
- Voice → STT → translate → inject → Task 2 + Task 4. ✓
- Telegram getUpdates/getFile → Task 3. ✓
- `telegram.inbound` config (translate default on, require_reply) → Task 1. ✓
- `chat_id` security boundary → Task 4 (`handleMessage` filter) + Task 6. ✓
- Single active inbound (lockfile) → Task 4 (`AcquireSingleFlight`) + Task 6. ✓
- Direct-exec fallback / error handling → Task 5 (`RunDirect`) + Task 6. ✓
- Outbound unchanged → no task touches the auto-speak/relay/Sender paths. ✓
- Docs + config example → Task 6. ✓
- Deferred (multi-session routing, permission-prompt notify, webhook) → not in any task, by design. ✓

**Placeholder scan:** No "TBD"/"add error handling here"; every code step is complete. The one flagged uncertainty (go-pty method names) is bounded by the Step 6 spike with a concrete fallback, not a placeholder.

**Type consistency:** `Inject(text string) error` is identical in `ptybridge.Bridge`, the `inbound.Injector` interface, and its test fake. `Transcribe`/`Translate` signatures match between `stt.Client`, `inbound.Transcriber`, and the test fake. `GetUpdates(ctx, offset, timeoutSec int)` and `DownloadVoice(ctx, fileID)` match between `telegram.Receiver` and the `inbound.Updater`/`VoiceDownloader` interfaces. `ResolvedInbound` fields are used consistently across Tasks 1/4/6.
