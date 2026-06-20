package ptybridge

import (
	"bytes"
	"testing"
)

func TestInject_WritesTextAndCarriageReturn(t *testing.T) {
	var buf bytes.Buffer
	b := &Bridge{w: &buf} // inject into a fake writer instead of a real PTY
	if err := b.Inject("hello there"); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	if got := buf.String(); got != "hello there\r" {
		t.Errorf("got %q, want %q", got, "hello there\r")
	}
}

func TestInject_StripsTrailingNewlinesBeforeCR(t *testing.T) {
	var buf bytes.Buffer
	b := &Bridge{w: &buf}
	_ = b.Inject("multi\nline\n")
	if got := buf.String(); got != "multi\nline\r" {
		t.Errorf("got %q", got)
	}
}
