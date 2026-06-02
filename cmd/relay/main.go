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
	"syscall"
	"time"

	"github.com/ybouhjira/claude-code-tts/internal/logging"
	"github.com/ybouhjira/claude-code-tts/internal/relay"
	"github.com/ybouhjira/claude-code-tts/internal/tts"
)

func main() {
	if err := logging.Init(); err != nil {
		log.Printf("Warning: failed to initialize file logging: %v", err)
	}

	logging.Info("========================================")
	logging.Info("TTS Relay Starting")
	logging.Info("PID: %d", os.Getpid())
	logging.Info("========================================")

	if os.Getenv("OPENAI_API_KEY") == "" {
		logging.Fatal("OPENAI_API_KEY environment variable is required")
	}

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
	ingestPort := os.Getenv("RELAY_PORT")
	if ingestPort == "" {
		ingestPort = "8765"
	}
	ingestAddr := "127.0.0.1:" + ingestPort

	publicPort := os.Getenv("PUBLIC_PORT")
	if publicPort == "" {
		publicPort = "8766"
	}

	// Print QR code so the user can scan from their phone.
	baseURL := fmt.Sprintf("http://%s:%s", lanIP(), publicPort)
	if err := relay.PrintQR(os.Stdout, baseURL, token); err != nil {
		logging.Info("QR generation failed: %v", err)
	}

	// Shared store and hub — both ingest and companion operate on the same data.
	store := relay.NewClipStore(10)
	hub := relay.NewSSEHub()
	synth := tts.NewClient()

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
	ingestSrv.Handler().
		WithTokenStore(ts).
		WithQRPrinter(func(newToken string) error {
			return relay.PrintQR(os.Stdout, baseURL, newToken)
		}).
		WithPushSender(ps).
		WithClipBaseURL(baseURL + "/" + token)

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
// "localhost" when no suitable interface is found.
func lanIP() string {
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if ipNet, ok := a.(*net.IPNet); ok && !ipNet.IP.IsLoopback() {
			if ip4 := ipNet.IP.To4(); ip4 != nil {
				return ip4.String()
			}
		}
	}
	return "localhost"
}
