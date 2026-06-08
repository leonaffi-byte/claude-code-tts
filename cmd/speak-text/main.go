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

func runSpeak(args []string) {
	fs := flag.NewFlagSet("speak-text", flag.ExitOnError)
	to := fs.String("to", "computer", "Where to deliver THIS clip: computer, phone, or both")
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

	dest := voicemode.Mode(*to)
	if dest != voicemode.Computer && dest != voicemode.Phone && dest != voicemode.Both {
		fmt.Fprintf(os.Stderr, "Error: -to must be computer, phone, or both\n")
		os.Exit(1)
	}

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

	if dest.SendsTelegram() && prov.DefaultFormat() != "mp3" {
		fmt.Fprintf(os.Stderr, "Error: telegram needs an MP3 provider (OpenAI or Grok), but %q emits %s\n", prov.Name(), prov.DefaultFormat())
		os.Exit(1)
	}

	out, err := prov.Synthesize(context.Background(), req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error synthesizing speech: %v\n", err)
		os.Exit(1)
	}

	if dest.SendsTelegram() {
		sender, reason := reg.TelegramSender()
		if sender == nil {
			fmt.Fprintf(os.Stderr, "Error: telegram not configured (%s)\n", reason)
			os.Exit(1)
		}
		if err := sender.SendAudio(context.Background(), out.Data, text); err != nil {
			fmt.Fprintf(os.Stderr, "Error sending to telegram: %v\n", err)
			os.Exit(1)
		}
	}
	if dest.PlaysLocal() {
		if err := audio.NewPlayer().Play(out.Data, out.Format); err != nil {
			fmt.Fprintf(os.Stderr, "Error playing audio: %v\n", err)
			os.Exit(1)
		}
	}
}
