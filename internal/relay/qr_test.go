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
