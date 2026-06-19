//go:build windows

package ptybridge

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestRoundTrip_InjectReachesPTY is the automated replacement for the manual
// ConPTY spike: it starts a real cmd.exe inside a ConPTY, injects an `echo`
// command, and asserts the injected marker is echoed back through the PTY.
// It is Windows-only and skipped under -short because it spawns a real process.
func TestRoundTrip_InjectReachesPTY(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping PTY round-trip under -short")
	}

	b := New()
	if err := b.Start("cmd.exe", []string{"/Q", "/K", "prompt $G"}); err != nil {
		t.Skipf("ConPTY unavailable: %v", err)
	}

	// Collect everything the PTY emits.
	collected := make(chan string, 1)
	go func() {
		var sb strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := b.pty.Read(buf)
			if n > 0 {
				sb.Write(buf[:n])
				if strings.Contains(sb.String(), "INJECTED_OK") {
					collected <- sb.String()
					return
				}
			}
			if err != nil {
				collected <- sb.String()
				return
			}
		}
	}()

	// Give the shell a moment to start, then inject a command that prints a marker.
	time.Sleep(500 * time.Millisecond)
	if err := b.Inject("echo INJECTED_OK"); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	select {
	case out := <-collected:
		if !strings.Contains(out, "INJECTED_OK") {
			t.Errorf("injected marker not echoed; got %q", out)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for injected marker to echo through PTY")
	}

	// Tell the shell to exit and clean up.
	_ = b.Inject("exit")
	_ = b.pty.Close()
}
