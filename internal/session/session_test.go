package session

import "testing"

func TestComputeLabel_EnvWins(t *testing.T) {
	t.Setenv("CLAUDE_TTS_SESSION", "my-session")
	if got := computeLabel(); got != "my-session" {
		t.Errorf("computeLabel() = %q, want my-session", got)
	}
}

func TestComputeLabel_CwdFallback(t *testing.T) {
	t.Setenv("CLAUDE_TTS_SESSION", "") // unset -> fall back to cwd base name
	got := computeLabel()
	// Running under `go test`, the cwd is this package's dir ("session").
	if got != "session" {
		t.Errorf("computeLabel() = %q, want the cwd base name 'session'", got)
	}
}

func TestLabel_Cached(t *testing.T) {
	// Label() must return a non-empty, stable value.
	if a, b := Label(), Label(); a == "" || a != b {
		t.Errorf("Label() not stable/non-empty: %q vs %q", a, b)
	}
}
