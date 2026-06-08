package relay

import (
	"context"
	"fmt"

	"github.com/ybouhjira/claude-code-tts/internal/tts"
)

// ProviderSynthesizer adapts a tts.Provider to the relay Synthesizer, enforcing
// MP3 output (the companion/clip store require MP3).
type ProviderSynthesizer struct {
	provider tts.Provider
	base     tts.Request
}

// NewProviderSynthesizer fails fast when the provider does not emit MP3.
func NewProviderSynthesizer(p tts.Provider, base tts.Request) (*ProviderSynthesizer, error) {
	if p.DefaultFormat() != "mp3" {
		return nil, fmt.Errorf("relay requires an MP3 provider; %q emits %s — use OpenAI or Grok for the relay", p.Name(), p.DefaultFormat())
	}
	return &ProviderSynthesizer{provider: p, base: base}, nil
}

func (s *ProviderSynthesizer) Synthesize(ctx context.Context, text string) ([]byte, error) {
	req := s.base
	req.Text = text
	out, err := s.provider.Synthesize(ctx, req)
	if err != nil {
		return nil, err
	}
	return out.Data, nil
}
