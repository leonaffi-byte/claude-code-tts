package tts

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// execCommand is indirected so tests can substitute a fake process.
var execCommand = exec.CommandContext

// PiperProvider synthesizes speech offline via the local piper binary.
type PiperProvider struct {
	binary   string
	modelDir string
}

// NewPiperProvider creates a provider. binary "" defaults to "piper".
func NewPiperProvider(binary, modelDir string) *PiperProvider {
	if binary == "" {
		binary = "piper"
	}
	return &PiperProvider{binary: binary, modelDir: expandHome(modelDir)}
}

func (p *PiperProvider) Name() string          { return "piper" }
func (p *PiperProvider) Voices() []string      { return nil } // any installed model
func (p *PiperProvider) DefaultFormat() string { return "wav" }

// Synthesize runs piper, writing WAV to a temp file and returning its bytes.
func (p *PiperProvider) Synthesize(ctx context.Context, req Request) (Audio, error) {
	if req.Voice == "" {
		return Audio{}, fmt.Errorf("piper: a voice (model name) is required")
	}
	model := req.Voice
	if !strings.HasSuffix(model, ".onnx") {
		model += ".onnx"
	}
	if !filepath.IsAbs(model) {
		model = filepath.Join(p.modelDir, model)
	}
	if _, err := os.Stat(model); err != nil {
		return Audio{}, fmt.Errorf("piper: model not found at %s: %w", model, err)
	}

	tmp, err := os.CreateTemp("", "piper-*.wav")
	if err != nil {
		return Audio{}, fmt.Errorf("piper: temp file: %w", err)
	}
	tmp.Close()
	defer os.Remove(tmp.Name())

	// length_scale is inversely proportional to speed (1.0 = normal).
	speed := ClampSpeed(req.Speed, 0.5, 2.0)
	lengthScale := strconv.FormatFloat(1.0/speed, 'f', 3, 64)

	cmd := execCommand(ctx, p.binary,
		"--model", model,
		"--output_file", tmp.Name(),
		"--length_scale", lengthScale,
	)
	cmd.Stdin = strings.NewReader(req.Text)
	if out, err := cmd.CombinedOutput(); err != nil {
		return Audio{}, fmt.Errorf("piper: run failed: %w (%s)", err, strings.TrimSpace(string(out)))
	}

	data, err := os.ReadFile(tmp.Name())
	if err != nil {
		return Audio{}, fmt.Errorf("piper: read output: %w", err)
	}
	return Audio{Data: data, Format: "wav"}, nil
}

func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}
