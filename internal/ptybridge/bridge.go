// Package ptybridge runs claude inside a pseudo-terminal so an external poller
// can inject input into the live session. It proxies the terminal transparently.
package ptybridge

import (
	"context"
	"io"
	"os"
	"strings"
	"sync"

	xpty "github.com/aymanbagabas/go-pty"
	"golang.org/x/term"
)

// Bridge owns a PTY hosting a child process and serializes writes to it so an
// injected reply never interleaves mid-line with the user's own keystrokes.
type Bridge struct {
	pty xpty.Pty
	cmd *xpty.Cmd
	mu  sync.Mutex
	w   io.Writer // the PTY (or a fake writer in tests)
}

// New creates an unstarted Bridge.
func New() *Bridge { return &Bridge{} }

// Start spawns name+args inside a new PTY. On any failure the caller should fall
// back to RunDirect.
func (b *Bridge) Start(name string, args []string) error {
	p, err := xpty.New()
	if err != nil {
		return err
	}
	c := p.Command(name, args...)
	if err := c.Start(); err != nil {
		_ = p.Close()
		return err
	}
	b.pty = p
	b.cmd = c
	b.w = p
	return nil
}

// Inject writes text followed by a carriage return into the PTY as if typed.
// Trailing newlines are trimmed so exactly one Enter is sent.
func (b *Bridge) Inject(text string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, err := io.WriteString(b.w, strings.TrimRight(text, "\r\n")+"\r")
	return err
}

// Proxy connects the user's terminal to the PTY (raw mode) and blocks until the
// child exits or ctx is cancelled.
func (b *Bridge) Proxy(ctx context.Context) error {
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err == nil {
		defer func() { _ = term.Restore(int(os.Stdin.Fd()), oldState) }()
	}
	if w, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
		_ = b.pty.Resize(w, h)
	}

	// PTY output -> our stdout.
	go func() { _, _ = io.Copy(os.Stdout, b.pty) }()
	// Our stdin -> PTY, but serialized with Inject via the mutex.
	go func() {
		bufr := make([]byte, 4096)
		for {
			n, rerr := os.Stdin.Read(bufr)
			if n > 0 {
				b.mu.Lock()
				_, werr := b.w.Write(bufr[:n])
				b.mu.Unlock()
				if werr != nil {
					return
				}
			}
			if rerr != nil {
				return
			}
		}
	}()

	done := make(chan error, 1)
	go func() { done <- b.cmd.Wait() }()
	select {
	case <-ctx.Done():
		_ = b.pty.Close()
		return ctx.Err()
	case err := <-done:
		_ = b.pty.Close()
		return err
	}
}

// Wait blocks until the child exits (used when Proxy is not driving the loop).
func (b *Bridge) Wait() error { return b.cmd.Wait() }

// RunDirect execs name+args attached directly to the current stdio. It is the
// fallback when a PTY cannot be created; inbound injection is unavailable here.
func RunDirect(name string, args []string) error {
	c := execCommand(name, args...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return c.Run()
}
