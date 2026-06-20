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
