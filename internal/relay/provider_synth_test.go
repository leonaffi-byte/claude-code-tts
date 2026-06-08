package relay

import (
	"context"
	"testing"

	"github.com/ybouhjira/claude-code-tts/internal/tts"
)

type stubProvider struct{ format string }

func (s stubProvider) Name() string          { return "stub" }
func (s stubProvider) Voices() []string      { return nil }
func (s stubProvider) DefaultFormat() string { return s.format }
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
