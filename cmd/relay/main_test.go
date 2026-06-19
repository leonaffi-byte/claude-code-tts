package main

import (
	"os"
	"os/exec"
	"testing"
)

// TestValidatePort_ValidAndDefault verifies the non-fatal paths of validatePort:
// an empty value falls back to the default, and a valid in-range numeric value
// is returned unchanged.
func TestValidatePort_ValidAndDefault(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		def  string
		want string
	}{
		{"empty uses default", "", "8765", "8765"},
		{"valid low boundary", "1", "8765", "1"},
		{"valid high boundary", "65535", "8765", "65535"},
		{"valid typical", "8766", "8765", "8766"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := validatePort("TEST_PORT", tc.raw, tc.def); got != tc.want {
				t.Errorf("validatePort(%q, %q) = %q, want %q", tc.raw, tc.def, got, tc.want)
			}
		})
	}
}

// TestValidatePort_InvalidExits verifies that an invalid port causes the process
// to exit non-zero (via logging.Fatal → os.Exit). Because os.Exit cannot be
// trapped in-process, this re-invokes the test binary as a subprocess with an
// env marker and asserts the subprocess exits with a failure status.
func TestValidatePort_InvalidExits(t *testing.T) {
	cases := map[string]string{
		"non-numeric": "abc",
		"zero":        "0",
		"negative":    "-1",
		"too large":   "70000",
	}
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			if os.Getenv("RELAY_VALIDATE_PORT_CRASH") == "1" {
				// Subprocess body: this must exit the process.
				validatePort("TEST_PORT", os.Getenv("RELAY_VALIDATE_PORT_VALUE"), "8765")
				return // unreachable on the fatal path
			}
			cmd := exec.Command(os.Args[0], "-test.run", "TestValidatePort_InvalidExits")
			cmd.Env = append(os.Environ(),
				"RELAY_VALIDATE_PORT_CRASH=1",
				"RELAY_VALIDATE_PORT_VALUE="+bad,
			)
			err := cmd.Run()
			if err == nil {
				t.Errorf("validatePort(%q) should have exited non-zero, but the subprocess succeeded", bad)
			}
			if _, ok := err.(*exec.ExitError); !ok && err != nil {
				t.Errorf("unexpected subprocess error type for %q: %v", bad, err)
			}
		})
	}
}
