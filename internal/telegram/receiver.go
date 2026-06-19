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
