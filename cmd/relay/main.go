package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/ybouhjira/claude-code-tts/internal/logging"
	"github.com/ybouhjira/claude-code-tts/internal/relay"
	"github.com/ybouhjira/claude-code-tts/internal/ttsconfig"
)

func main() {
	if err := logging.Init(); err != nil {
		log.Printf("Warning: failed to initialize file logging: %v", err)
	}

	logging.Info("========================================")
	logging.Info("TTS Relay Starting")
	logging.Info("PID: %d", os.Getpid())
	logging.Info("========================================")

	// API keys are validated per-provider at resolve time (see internal/ttsconfig).
	// Do not hard-require OPENAI_API_KEY here — a Grok-only relay needs no OpenAI key.

	home, err := os.UserHomeDir()
	if err != nil {
		logging.Fatal("cannot determine home directory: %v", err)
	}

	// Load or generate persistent token.
	tokenPath := filepath.Join(home, ".claude", "plugins", "claude-code-tts", "token")
	ts := relay.NewTokenStore(tokenPath)
	token, err := ts.LoadOrGenerate()
	if err != nil {
		logging.Fatal("failed to load/generate token: %v", err)
	}

	// Ingest server binds to loopback only — never exposed on 0.0.0.0.
	ingestPort := validatePort("RELAY_PORT", os.Getenv("RELAY_PORT"), "8765")
	ingestAddr := "127.0.0.1:" + ingestPort

	publicPort := validatePort("PUBLIC_PORT", os.Getenv("PUBLIC_PORT"), "8766")

	// Print QR code so the user can scan from their phone.
	baseURL := fmt.Sprintf("http://%s:%s", lanIP(), publicPort)
	if err := relay.PrintQR(os.Stdout, baseURL, token); err != nil {
		logging.Info("QR generation failed: %v", err)
	}

	// Shared store and hub — both ingest and companion operate on the same data.
	store := relay.NewClipStore(10)
	hub := relay.NewSSEHub()

	reg := ttsconfig.LoadOrDefault()
	provider, baseReq, err := reg.Default()
	if err != nil {
		logging.Fatal("failed to resolve TTS profile: %v", err)
	}
	synth, err := relay.NewProviderSynthesizer(provider, baseReq)
	if err != nil {
		logging.Fatal("%v", err) // WAV provider configured for relay -> fail fast
	}

	ingestSrv, err := relay.NewServer(ingestAddr, synth, store, hub)
	if err != nil {
		logging.Fatal("failed to create ingest server: %v", err)
	}

	// Load or generate VAPID keypair for Web Push.
	configDir := filepath.Join(home, ".claude", "plugins", "claude-code-tts")
	vs := relay.NewVAPIDStore(configDir)
	if _, _, err := vs.LoadOrGenerate(); err != nil {
		logging.Fatal("failed to load/generate VAPID keypair: %v", err)
	}
	pushTransport := relay.NewWebPushTransport(vs)
	ps := relay.NewPushSender(pushTransport)

	// Wire token store and QR printer into the ingest handler for /rotate-token.
	//
	// Note: we intentionally do NOT pass a clip base URL containing the secret
	// token. Embedding the token in the push payload URL would ship the sole
	// auth secret to third-party push services and persist it in the phone's
	// notification store. Instead the push payload carries only the clip ID, and
	// the service worker resolves the clip URL relative to its own token-scoped
	// registration scope (see web/sw.js playClip(clipId)).
	ingestSrv.Handler().
		WithTokenStore(ts).
		WithQRPrinter(func(newToken string) error {
			return relay.PrintQR(os.Stdout, baseURL, newToken)
		}).
		WithPushSender(ps)

	companion := relay.NewCompanionHandler(store, hub, ts).
		WithPushSender(ps).
		WithVAPIDStore(vs)
	pubSrv := relay.NewPublicServer(ts, companion)

	// Graceful shutdown on SIGINT/SIGTERM.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-quit
		logging.Info("shutting down relay...")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := ingestSrv.Shutdown(ctx); err != nil {
			logging.Error("ingest server shutdown error: %v", err)
		}
		if err := pubSrv.Shutdown(ctx); err != nil {
			logging.Error("public server shutdown error: %v", err)
		}
		os.Exit(0)
	}()

	go func() {
		logging.Info("ingest server listening on %s", ingestAddr)
		if err := ingestSrv.Start(); err != nil && err != http.ErrServerClosed {
			logging.Fatal("ingest server error: %v", err)
		}
	}()

	logging.Info("public server listening on 0.0.0.0:%s", publicPort)
	if err := pubSrv.Serve("0.0.0.0:" + publicPort); err != nil && err != http.ErrServerClosed {
		logging.Fatal("public server error: %v", err)
	}
}

// lanIP returns the first non-loopback IPv4 address found on the host, or
// "localhost" when no suitable interface is found. It logs a warning when the
// interface lookup fails or no usable LAN IPv4 exists so that a fallback to an
// unreachable "localhost" QR is observable to the operator.
func lanIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		logging.Warn("could not enumerate network interfaces (%v); QR will point to localhost and may be unreachable from a phone", err)
		return "localhost"
	}
	for _, a := range addrs {
		if ipNet, ok := a.(*net.IPNet); ok && !ipNet.IP.IsLoopback() {
			if ip4 := ipNet.IP.To4(); ip4 != nil {
				return ip4.String()
			}
		}
	}
	logging.Warn("no non-loopback LAN IPv4 found; QR will point to localhost and may be unreachable from a phone")
	return "localhost"
}

// validatePort validates a port string read from the environment. When raw is
// empty the provided default is used. A non-numeric or out-of-range value is a
// fatal startup error so the operator gets a clear message before any listener
// is started or the QR is printed. It returns the validated port as a string.
func validatePort(name, raw, def string) string {
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		logging.Fatal("invalid %s %q: must be a number between 1 and 65535", name, raw)
	}
	if n < 1 || n > 65535 {
		logging.Fatal("invalid %s %d: must be between 1 and 65535", name, n)
	}
	return raw
}
