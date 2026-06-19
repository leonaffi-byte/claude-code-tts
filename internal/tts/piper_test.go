package tts

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeExecCommand re-invokes the test binary as the "piper" process.
func fakeExecCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	cs := append([]string{"-test.run=TestPiperHelperProcess", "--", name}, args...)
	cmd := exec.CommandContext(ctx, os.Args[0], cs...)
	cmd.Env = append(os.Environ(), "GO_WANT_PIPER_HELPER=1")
	return cmd
}

// TestPiperHelperProcess is not a real test; it emulates the piper binary:
// it finds --output_file <path> and writes fake WAV bytes there.
func TestPiperHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_PIPER_HELPER") != "1" {
		return
	}
	args := os.Args
	for i := len(args) - 1; i >= 0; i-- {
		if args[i] == "--" {
			args = args[i+1:]
			break
		}
	}
	var out string
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--output_file" {
			out = args[i+1]
		}
	}
	if out == "" {
		os.Exit(2)
	}
	// Record the full arg list so the parent test can assert how piper was invoked.
	if argsFile := os.Getenv("PIPER_ARGS_FILE"); argsFile != "" {
		_ = os.WriteFile(argsFile, []byte(strings.Join(args, "\n")), 0o600)
	}
	_ = os.WriteFile(out, []byte("RIFFfakeWAV"), 0o600)
	os.Exit(0)
}

func TestPiperProvider_Synthesize(t *testing.T) {
	// Mutating the package-level execCommand is not safe under t.Parallel().
	execCommand = fakeExecCommand
	defer func() { execCommand = exec.CommandContext }()

	// The provider stats the model file before running piper, so create a
	// dummy model in the same dir we pass as modelDir (t.TempDir() returns a
	// fresh dir each call — capture it once).
	modelDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(modelDir, "en_US-amy-medium.onnx"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	argsFile := filepath.Join(modelDir, "args.txt")
	t.Setenv("PIPER_ARGS_FILE", argsFile)

	p := NewPiperProvider("piper", modelDir)
	got, err := p.Synthesize(context.Background(), Request{Text: "hello", Voice: "en_US-amy-medium", Speed: 1.0})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if !strings.HasPrefix(string(got.Data), "RIFF") {
		t.Errorf("data = %q, want WAV", got.Data)
	}
	if got.Format != "wav" {
		t.Errorf("format = %q, want wav", got.Format)
	}

	// Verify how piper was actually invoked: resolved model path and the
	// length_scale = 1/speed mapping (speed 1.0 -> length_scale 1.000).
	rawArgs, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read args file: %v", err)
	}
	gotArgs := string(rawArgs)
	if !strings.Contains(gotArgs, filepath.Join(modelDir, "en_US-amy-medium.onnx")) {
		t.Errorf("piper args missing resolved model path; got:\n%s", gotArgs)
	}
	if !strings.Contains(gotArgs, "--length_scale\n1.000") {
		t.Errorf("piper args missing '--length_scale 1.000'; got:\n%s", gotArgs)
	}
}

func TestPiperProvider_RejectsPathVoice(t *testing.T) {
	// A voice containing a path (absolute or with separators) must be rejected
	// before any model lookup, so Piper can only load models from modelDir.
	p := NewPiperProvider("piper", t.TempDir())
	cases := []string{
		"/etc/passwd",
		"/abs/model.onnx",
		"sub/dir/model",
		"sub" + string(filepath.Separator) + "model",
	}
	for _, voice := range cases {
		if _, err := p.Synthesize(context.Background(), Request{Text: "hi", Voice: voice}); err == nil {
			t.Errorf("voice %q: expected rejection, got nil error", voice)
		}
	}
}

func TestPiperProvider_Metadata(t *testing.T) {
	p := NewPiperProvider("piper", "/models")
	if p.Name() != "piper" || p.DefaultFormat() != "wav" {
		t.Errorf("metadata wrong: %q/%q", p.Name(), p.DefaultFormat())
	}
	if len(p.Voices()) != 0 {
		t.Errorf("Voices should be empty (any), got %v", p.Voices())
	}
}
