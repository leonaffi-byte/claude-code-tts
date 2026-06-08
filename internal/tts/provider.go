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
	// Voices returns the known voice ids. A nil/empty slice means the provider
	// accepts arbitrary voice strings (e.g. file-based models like Piper).
	Voices() []string
	DefaultFormat() string // "mp3" | "wav"
	Synthesize(ctx context.Context, req Request) (Audio, error)
}

// ClampSpeed returns speed clamped to [min,max]. A speed of 0 means "use the
// provider default" (1.0), which is then clamped to [min,max] like any other
// value, so callers whose range excludes 1.0 still get an in-range result.
func ClampSpeed(speed, min, max float64) float64 {
	if speed == 0 {
		speed = 1.0
	}
	if speed < min {
		return min
	}
	if speed > max {
		return max
	}
	return speed
}
