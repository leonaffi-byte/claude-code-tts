// internal/tts/provider.go
package tts

import "context"

// Request is a provider-agnostic synthesis request.
type Request struct {
	Text  string
	Voice string  // provider-scoped voice id (e.g. "alloy", "eve", or a Piper model name)
	Speed float64 // 0 = provider default; otherwise clamped to the provider's range
	Model string  // optional, provider-specific (OpenAI only); "" = provider default
}

// Audio is synthesized audio plus its container format.
type Audio struct {
	Data   []byte
	Format string // "mp3" | "wav"
}

// Provider converts text to speech.
type Provider interface {
	Name() string
	Voices() []string      // known voice ids; empty means "any" (e.g. Piper)
	DefaultFormat() string // "mp3" | "wav"
	Synthesize(ctx context.Context, req Request) (Audio, error)
}

// ClampSpeed returns speed clamped to [min,max]. A speed of 0 means "use the
// provider default" and returns 1.0.
func ClampSpeed(speed, min, max float64) float64 {
	if speed == 0 {
		return 1.0
	}
	if speed < min {
		return min
	}
	if speed > max {
		return max
	}
	return speed
}
