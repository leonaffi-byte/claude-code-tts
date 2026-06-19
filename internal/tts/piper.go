package tts

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// execCommand is indirected so tests can substitute a fake process.
var execCommand = exec.CommandContext

// piperTimeout bounds a single piper invocation when the caller's context has
// no deadline of its own. Callers currently pass context.Background(), so
// without this a hung piper binary would block a worker indefinitely.
const piperTimeout = 60 * time.Second

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
	// The voice is a model name confined to modelDir; reject absolute paths and
	// path separators so a caller-controlled voice can't load a model from an
	// arbitrary location on the filesystem.
	if filepath.IsAbs(req.Voice) || strings.ContainsRune(req.Voice, '/') || strings.ContainsRune(req.Voice, filepath.Separator) {
		return Audio{}, fmt.Errorf("piper: invalid voice %q: must be a bare model name, not a path", req.Voice)
	}
	model := req.Voice
	if !strings.HasSuffix(model, ".onnx") {
		model += ".onnx"
	}
	model = filepath.Join(p.modelDir, model)
	if _, err := os.Stat(model); err != nil {
		return Audio{}, fmt.Errorf("piper: model not found at %s: %w", model, err)
	}

	// Every current caller passes a context without a deadline; bound the
	// subprocess so a hung piper binary can't wedge a worker forever.
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, piperTimeout)
		defer cancel()
	}

	tmp, err := os.CreateTemp("", "piper-*.wav")
	if err != nil {
		return Audio{}, fmt.Errorf("piper: temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name()) //nolint:errcheck
		return Audio{}, fmt.Errorf("piper: temp file: %w", err)
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck

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
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Audio{}, fmt.Errorf("piper: run timed out or was cancelled: %w (%s)", ctxErr, strings.TrimSpace(string(out)))
		}
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
