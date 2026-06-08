package telegram

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
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

	// The URL embeds the bot token, so any error carrying it (notably the
	// *url.Error from Do) must be redacted before it reaches a caller or log.
	url := fmt.Sprintf("%s/bot%s/sendAudio", s.baseURL, s.token)
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

// redact removes the bot token from a string so it never leaks into an error
// message or log line.
func (s *Sender) redact(msg string) string {
	if s.token == "" {
		return msg
	}
	return strings.ReplaceAll(msg, s.token, "REDACTED")
}
