// Command claude-run launches `claude` inside a PTY and injects Telegram replies
// into the live session. Invoke as `claude-tts run [claude args...]`; alias
// `claude` to it for a transparent experience.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/ybouhjira/claude-code-tts/internal/inbound"
	"github.com/ybouhjira/claude-code-tts/internal/ptybridge"
	"github.com/ybouhjira/claude-code-tts/internal/stt"
	"github.com/ybouhjira/claude-code-tts/internal/telegram"
	"github.com/ybouhjira/claude-code-tts/internal/ttsconfig"
)

func main() {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "run" {
		args = args[1:] // allow both `claude-run ...` and `claude-tts run ...`
	}

	bridge := ptybridge.New()
	if err := bridge.Start("claude", args); err != nil {
		// PTY unavailable: degrade to a plain claude with no inbound.
		fmt.Fprintf(os.Stderr, "claude-tts: PTY unavailable (%v); running claude directly without Telegram inbound\n", err)
		if rerr := ptybridge.RunDirect("claude", args); rerr != nil {
			os.Exit(1)
		}
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if release := startInbound(ctx, bridge); release != nil {
		defer func() { _ = release() }()
	}

	if err := bridge.Proxy(ctx); err != nil && ctx.Err() == nil {
		fmt.Fprintf(os.Stderr, "claude-tts: %v\n", err)
		os.Exit(1)
	}
}

// startInbound launches the Telegram poller if inbound is enabled, the bot token
// and chat id resolve, and this process wins the single-flight lock. It returns a
// release func for the lock (nil when inbound did not start).
func startInbound(ctx context.Context, bridge *ptybridge.Bridge) func() error {
	cfg, err := ttsconfig.LoadConfigOrDefault()
	if err != nil {
		return nil
	}
	if cfg.Telegram == nil {
		return nil
	}
	in := cfg.Telegram.ResolvedInbound()
	if !in.Enabled {
		return nil
	}
	token := os.Getenv(envOr(cfg.Telegram.BotTokenEnv, "TELEGRAM_BOT_TOKEN"))
	if token == "" || cfg.Telegram.ChatID == "" {
		fmt.Fprintln(os.Stderr, "claude-tts: telegram inbound enabled but token/chat_id missing; skipping")
		return nil
	}
	chatID, err := strconv.ParseInt(cfg.Telegram.ChatID, 10, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "claude-tts: invalid chat_id %q\n", cfg.Telegram.ChatID)
		return nil
	}
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "claude-tts: OPENAI_API_KEY unset; voice transcription unavailable")
	}

	lockPath := filepath.Join(stateDir(), "inbound.lock")
	release, ok, lerr := inbound.AcquireSingleFlight(lockPath)
	if lerr != nil || !ok {
		fmt.Fprintln(os.Stderr, "claude-tts: another session owns Telegram inbound; running proxy-only")
		return nil
	}

	recv := telegram.NewReceiver(token)
	sttClient := stt.New(apiKey)
	poller := inbound.NewPoller(recv, recv, sttClient, bridge, chatID, in)
	go func() { _ = poller.Run(ctx) }()
	return release
}

func envOr(name, fallback string) string {
	if name != "" {
		return name
	}
	return fallback
}

func stateDir() string {
	if p := os.Getenv("CLAUDE_TTS_STATE"); p != "" {
		return filepath.Dir(p)
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "plugins", "claude-code-tts")
}
