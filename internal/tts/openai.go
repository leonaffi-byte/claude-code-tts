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
func (p *OpenAIProvider) Voices() []string      { return openAIVoices() }
func (p *OpenAIProvider) DefaultFormat() string { return "mp3" }

type openAIRequest struct {
	Model          string  `json:"model"`
	Input          string  `json:"input"`
	Voice          string  `json:"voice"`
	Speed          float64 `json:"speed,omitempty"`
	ResponseFormat string  `json:"response_format,omitempty"`
}

// Synthesize converts text to audio. Format "opus" requests Ogg/Opus (for
// Telegram voice messages); anything else yields MP3.
func (p *OpenAIProvider) Synthesize(ctx context.Context, req Request) (Audio, error) {
	if p.apiKey == "" {
		return Audio{}, fmt.Errorf("openai: OPENAI_API_KEY is not set")
	}
	model := p.model
	if req.Model != "" {
		model = req.Model
	}
	outFmt := "mp3"
	if req.Format == "opus" {
		outFmt = "opus"
	}
	body := openAIRequest{
		Model:          model,
		Input:          req.Text,
		Voice:          req.Voice,
		Speed:          ClampSpeed(req.Speed, 0.25, 4.0),
		ResponseFormat: outFmt,
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
	return Audio{Data: audio, Format: outFmt}, nil
}
