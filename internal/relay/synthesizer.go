package relay

import "context"

// Synthesizer converts text to MP3 bytes for the relay/companion path.
type Synthesizer interface {
	Synthesize(ctx context.Context, text string) ([]byte, error)
}
