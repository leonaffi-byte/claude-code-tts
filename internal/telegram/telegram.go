package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
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

// Send delivers audio to the chat, choosing the message type by format:
// "opus" → a voice message (the round voice bubble, via sendVoice); anything
// else → a tappable audio file (via sendAudio). caption is optional.
func (s *Sender) Send(ctx context.Context, audio []byte, format, caption string) error {
	if format == "opus" {
		return s.sendFile(ctx, "sendVoice", "voice", "clip.ogg", audio, caption, nil)
	}
	return s.sendFile(ctx, "sendAudio", "audio", "clip.mp3", audio, caption, nil)
}

// SendAudio uploads audio (MP3) to the chat as a tappable audio message.
func (s *Sender) SendAudio(ctx context.Context, audio []byte, caption string) error {
	return s.sendFile(ctx, "sendAudio", "audio", "clip.mp3", audio, caption, nil)
}

// SendVoiceWithButton sends an Opus voice message with an inline keyboard.
func (s *Sender) SendVoiceWithButton(ctx context.Context, audio []byte, caption string, keyboard [][]InlineButton) error {
	return s.sendFile(ctx, "sendVoice", "voice", "clip.ogg", audio, caption, keyboard)
}

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
	payload := map[string]any{
		"chat_id": s.chatID,
		"text":    text,
	}
	if len(keyboard) > 0 {
		payload["reply_markup"] = map[string]any{"inline_keyboard": keyboard}
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("telegram: marshal sendMessage: %w", err)
	}
	u := fmt.Sprintf("%s/bot%s/sendMessage", s.baseURL, s.token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("telegram: create request: %s", s.redact(err.Error()))
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("telegram: sendMessage failed: %s", s.redact(err.Error()))
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		rb, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("telegram: sendMessage error (status %d): %s", resp.StatusCode, s.redact(string(rb)))
	}
	return nil
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

// sendFile POSTs a multipart upload to the given Bot API method, attaching data
// under the given form field + filename. An optional inline keyboard can be attached.
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

	// The URL embeds the bot token, so any error carrying it (notably the
	// *url.Error from Do) must be redacted before it reaches a caller or log.
	apiURL := fmt.Sprintf("%s/bot%s/%s", s.baseURL, s.token, method)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, &body)
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
