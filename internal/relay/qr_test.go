package relay

import (
	"strings"
	"testing"
)

func TestPrintQR_WritesURLAndNonEmptyOutput(t *testing.T) {
	var sb strings.Builder
	err := PrintQR(&sb, "http://192.168.1.5:8766", "mytoken")
	if err != nil {
		t.Fatalf("PrintQR returned error: %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "http://192.168.1.5:8766/mytoken/") {
		t.Errorf("expected output to contain URL, got: %q", out)
	}
	if len(out) < 100 {
		t.Errorf("expected substantial QR output, got only %d chars", len(out))
	}
}

// TestPrintQR_EmptyToken_ReturnsNoErrorWithContent verifies that PrintQR
// succeeds when the token is an empty string and still writes non-trivial output
// (the QR code wraps the base URL with a trailing slash).
func TestPrintQR_EmptyToken_ReturnsNoErrorWithContent(t *testing.T) {
	var sb strings.Builder
	err := PrintQR(&sb, "http://192.168.1.5:8766", "")
	if err != nil {
		t.Fatalf("PrintQR with empty token returned unexpected error: %v", err)
	}

	out := sb.String()
	if len(out) == 0 {
		t.Error("PrintQR with empty token: expected non-empty output")
	}
	// The URL should still be present; with an empty token it becomes baseURL + "//"
	if !strings.Contains(out, "http://192.168.1.5:8766") {
		t.Errorf("PrintQR with empty token: output does not contain base URL, got: %q", out)
	}
}

// TestPrintQR_OutputContainsBothURLAndQRContent verifies that the output contains
// the companion URL line AND substantial QR-code content (length > 50 chars
// excluding the URL line alone).
func TestPrintQR_OutputContainsBothURLAndQRContent(t *testing.T) {
	var sb strings.Builder
	const base = "http://10.0.0.1:8766"
	const token = "sometoken"
	if err := PrintQR(&sb, base, token); err != nil {
		t.Fatalf("PrintQR returned error: %v", err)
	}

	out := sb.String()
	expectedURL := base + "/" + token + "/"

	if !strings.Contains(out, expectedURL) {
		t.Errorf("output does not contain companion URL %q; got: %q", expectedURL, out)
	}

	// Verify the QR-code block adds non-trivial content beyond the URL line.
	urlLineLen := len("Companion URL: " + expectedURL + "\n\n")
	if len(out) <= urlLineLen+50 {
		t.Errorf("output appears to lack QR code content: total len=%d, url-line len=%d", len(out), urlLineLen)
	}
}
