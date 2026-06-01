package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
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

	port := os.Getenv("RELAY_PORT")
	if port == "" {
		port = "8765"
	}
	// Bind to loopback only — never expose on 0.0.0.0.
	addr := "127.0.0.1:" + port

	synth := tts.NewClient()

	srv, err := relay.NewServer(addr, synth, 10)
	if err != nil {
		logging.Fatal("failed to create relay server: %v", err)
	}

	// Graceful shutdown on SIGINT/SIGTERM.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-quit
		logging.Info("shutting down relay...")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			logging.Error("shutdown error: %v", err)
		}
		os.Exit(0)
	}()

	logging.Info("relay listening on %s", addr)
	if err := srv.Start(); err != nil && err != http.ErrServerClosed {
		logging.Fatal("relay server error: %v", err)
	}
}
