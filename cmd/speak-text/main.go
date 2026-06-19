package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/ybouhjira/claude-code-tts/internal/audio"
	"github.com/ybouhjira/claude-code-tts/internal/tts"
	"github.com/ybouhjira/claude-code-tts/internal/ttsconfig"
	"github.com/ybouhjira/claude-code-tts/internal/voicemode"
)

func main() {
	args := os.Args[1:]
	switch {
	case len(args) >= 1 && args[0] == "mode":
		runMode(args[1:])
	case len(args) >= 1 && args[0] == "status":
		runStatus()
	default:
		runSpeak(args)
	}
}

func runMode(args []string) {
	store := voicemode.DefaultStore()
	if len(args) == 0 {
		fmt.Printf("voice mode: %s\n", store.Get())
		return
	}
	m := voicemode.Mode(args[0])
	if err := store.Set(m); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("voice mode set to: %s\n", m)
}

func runStatus() {
	fmt.Printf("voice mode: %s\n", voicemode.DefaultStore().Get())
	if _, reason := ttsconfig.LoadOrDefault().TelegramSender(); reason == "" {
		fmt.Println("telegram: configured")
	} else {
		fmt.Printf("telegram: not configured (%s)\n", reason)
	}
}

// applyOverrides applies the -voice and -speed CLI overrides to a request that
// was resolved via a profile (or the default selection). The -voice override is
// validated against the provider's voice list so an invalid voice fails locally
// with a clear message instead of being sent to the remote API. Providers with
// no fixed voice list (e.g. Piper) accept arbitrary voices and skip the check.
func applyOverrides(req *tts.Request, prov tts.Provider, voice string, speed float64) error {
	if voice != "" {
		if voices := prov.Voices(); len(voices) > 0 && !contains(voices, voice) {
			return fmt.Errorf("voice %q is not valid for provider %q (valid: %v)", voice, prov.Name(), voices)
		}
		req.Voice = voice
	}
	if speed != 0 {
		req.Speed = speed
	}
	return nil
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func runSpeak(args []string) {
	fs := flag.NewFlagSet("speak-text", flag.ExitOnError)
	to := fs.String("to", "", "Where to deliver THIS clip: computer, phone, or both (default: saved voice mode)")
	providerFlag := fs.String("provider", "", "Use an explicit provider (openai, grok, piper) instead of a profile")
	profile := fs.String("profile", "", "Voice profile from config (default: configured default / CLAUDE_TTS_* env)")
	voice := fs.String("voice", "", "Override the profile's voice")
	speed := fs.Float64("speed", 0, "Override speech speed (0 = profile default)")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage:\n")
		fmt.Fprintf(os.Stderr, "  %s [OPTIONS] TEXT        speak text now\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s mode [off|computer|phone|both]   show or set the saved voice mode\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s status                show voice mode + telegram status\n\n", os.Args[0])
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)
	if fs.NArg() == 0 {
		fs.Usage()
		os.Exit(1)
	}
	text := fs.Arg(0)

	// Determine whether -to was explicitly passed. When it was not, fall back to
	// the saved voice mode (voicemode.DefaultStore().Get()) so the CLI honors a
	// previously set `speak-text mode ...`, matching the MCP/worker path. -to is
	// documented as a one-off override that does NOT change the saved mode and,
	// per the README, accepts only computer/phone/both (not off).
	toSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "to" {
			toSet = true
		}
	})

	var dest voicemode.Mode
	if toSet {
		dest = voicemode.Mode(*to)
		if dest != voicemode.Computer && dest != voicemode.Phone && dest != voicemode.Both {
			fmt.Fprintf(os.Stderr, "Error: -to must be computer, phone, or both\n")
			os.Exit(1)
		}
	} else {
		dest = voicemode.DefaultStore().Get()
		// Off: honor the saved mode by producing no audio (no synthesis cost,
		// no delivery), mirroring the worker's `off` handling.
		if dest == voicemode.Off {
			return
		}
	}

	reg := ttsconfig.LoadOrDefault()

	var prov tts.Provider
	var req tts.Request
	var err error
	switch {
	case *providerFlag != "":
		// ResolveVoice validates *voice against the provider's voice list, so the
		// resolved request already carries a vetted voice.
		prov, req, err = reg.ResolveVoice(*providerFlag, *voice, *speed)
		if err == nil && *speed != 0 {
			req.Speed = *speed
		}
	case *profile != "":
		prov, req, err = reg.Resolve(*profile)
		if err == nil {
			err = applyOverrides(&req, prov, *voice, *speed)
		}
	default:
		prov, req, err = reg.Default()
		if err == nil {
			err = applyOverrides(&req, prov, *voice, *speed)
		}
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	req.Text = text

	if dest.SendsTelegram() && prov.DefaultFormat() != "mp3" {
		fmt.Fprintf(os.Stderr, "Error: telegram needs an MP3 provider (OpenAI or Grok), but %q emits %s\n", prov.Name(), prov.DefaultFormat())
		os.Exit(1)
	}

	if dest.SendsTelegram() {
		sender, reason := reg.TelegramSender()
		if sender == nil {
			fmt.Fprintf(os.Stderr, "Error: telegram not configured (%s)\n", reason)
			os.Exit(1)
		}
		tgReq := req
		tgReq.Format = "opus" // voice message when the provider can emit Opus
		tgOut, err := prov.Synthesize(context.Background(), tgReq)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error synthesizing speech: %v\n", err)
			os.Exit(1)
		}
		if err := sender.Send(context.Background(), tgOut.Data, tgOut.Format, text); err != nil {
			fmt.Fprintf(os.Stderr, "Error sending to telegram: %v\n", err)
			os.Exit(1)
		}
	}
	if dest.PlaysLocal() {
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
}
