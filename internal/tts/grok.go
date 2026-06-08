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

func (p *GrokProvider) Name() string          { return "grok" }
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
