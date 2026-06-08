package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/ybouhjira/claude-code-tts/internal/audio"
	"github.com/ybouhjira/claude-code-tts/internal/tts"
	"github.com/ybouhjira/claude-code-tts/internal/ttsconfig"
)

func main() {
	providerFlag := flag.String("provider", "", "Use an explicit provider (openai, grok, piper) instead of a profile")
	profile := flag.String("profile", "", "Voice profile from config (default: the configured default profile / CLAUDE_TTS_* env)")
	voice := flag.String("voice", "", "Override the profile's voice")
	speed := flag.Float64("speed", 0, "Override speech speed (0 = profile default)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [OPTIONS] TEXT\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Converts text to speech via the configured provider and plays it.\n\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nExamples:\n")
		fmt.Fprintf(os.Stderr, "  %s \"Build completed\"\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s -profile error \"Build failed\"\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s -provider grok -voice leo \"Build failed\"\n", os.Args[0])
	}
	flag.Parse()

	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(1)
	}
	text := flag.Arg(0)

	reg := ttsconfig.LoadOrDefault()

	var prov tts.Provider
	var req tts.Request
	var err error
	switch {
	case *providerFlag != "":
		prov, req, err = reg.ResolveVoice(*providerFlag, *voice, *speed)
	case *profile != "":
		prov, req, err = reg.Resolve(*profile)
	default:
		// No explicit profile/provider: honor CLAUDE_TTS_* env + configured default.
		prov, req, err = reg.Default()
	}
	if err == nil {
		if *voice != "" {
			req.Voice = *voice
		}
		if *speed != 0 {
			req.Speed = *speed
		}
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	req.Text = text

	out, err := prov.Synthesize(context.Background(), req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error synthesizing speech: %v\n", err)
		os.Exit(1)
	}

	if err := audio.NewPlayer().Play(out.Data, out.Format); err != nil {
		fmt.Fprintf(os.Stderr, "Error playing audio: %v\n", err)
		os.Exit(1)
	}
}
