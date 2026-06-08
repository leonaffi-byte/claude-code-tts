package main

import (
	"log"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/ybouhjira/claude-code-tts/internal/logging"
	"github.com/ybouhjira/claude-code-tts/internal/server"
)

func main() {
	// Initialize file logging
	if err := logging.Init(); err != nil {
		log.Printf("Warning: failed to initialize file logging: %v", err)
	}

	logging.Info("========================================")
	logging.Info("TTS Server Starting")
	logging.Info("Go version: %s", runtime.Version())
	logging.Info("OS/Arch: %s/%s", runtime.GOOS, runtime.GOARCH)
	logging.Info("PID: %d", os.Getpid())
	logging.Info("Log file: %s", logging.GetLogPath())
	logging.Info("========================================")

	// API keys are validated per-provider at resolve time (see internal/ttsconfig).
	// Do not hard-require OPENAI_API_KEY here — Grok/Piper configs need no OpenAI key.

	// Create and start the MCP server
	srv, err := server.New()
	if err != nil {
		logging.Fatal("Failed to create server: %v", err)
	}

	// Handle graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGPIPE)

	go func() {
		sig := <-sigChan
		logging.Info("Received signal: %v", sig)
		logging.Info("Shutting down TTS server...")
		srv.Shutdown()
		logging.Info("TTS Server stopped gracefully")
		os.Exit(0)
	}()

	// Start serving
	logging.Info("Starting MCP stdio server...")
	if err := srv.Start(); err != nil {
		logging.Error("Server error: %v", err)
		logging.Fatal("Server stopped unexpectedly")
	}

	logging.Info("Server ended normally")
}
