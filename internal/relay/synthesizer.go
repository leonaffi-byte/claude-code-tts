package relay

import "github.com/ybouhjira/claude-code-tts/internal/tts"

// Synthesizer is the contract the handler needs to convert text to MP3 bytes.
// tts.Client satisfies this interface; tests inject a mock.
type Synthesizer interface {
	Synthesize(text string, voice tts.Voice) ([]byte, error)
}
