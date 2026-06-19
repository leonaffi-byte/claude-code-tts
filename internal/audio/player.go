package audio

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
)

// commandBuilder builds the platform command to play a file of the given
// format. It matches the signature of buildPlayCommand so it can be swapped
// out in tests.
type commandBuilder func(goos, format, path string) (*exec.Cmd, error)

// commandRunner runs a built command and returns its result. The default
// runner captures combined output so playback failures carry the underlying
// player diagnostics. Tests inject a fake to avoid spawning real processes.
type commandRunner func(cmd *exec.Cmd) error

// Player handles audio playback with mutex protection.
type Player struct {
	// playMu serializes playback: only one audio plays at a time. It is held
	// for the entire CreateTemp/Write/Run sequence, which can last the whole
	// audio duration.
	playMu sync.Mutex
	// isPlaying is a short, lock-free status flag. It is read by IsPlaying()
	// which must return immediately, so it MUST NOT share playMu (that lock is
	// held for the entire playback). atomic.Bool gives an instant read of the
	// in-flight state.
	isPlaying atomic.Bool

	// build and run are injectable seams for hermetic testing. They default to
	// the real platform command builder/runner.
	build commandBuilder
	run   commandRunner
}

// NewPlayer creates a new audio player.
func NewPlayer() *Player {
	return &Player{
		build: buildPlayCommand,
		run:   runCommand,
	}
}

// runCommand executes cmd, capturing combined stdout+stderr so that a player
// failure (unsupported codec, missing file, a PowerShell exception, etc.) is
// included in the returned error instead of being discarded.
func runCommand(cmd *exec.Cmd) error {
	out, err := cmd.CombinedOutput()
	if err != nil {
		if trimmed := strings.TrimSpace(string(out)); trimmed != "" {
			return fmt.Errorf("%w (%s)", err, trimmed)
		}
		return err
	}
	return nil
}

// supportedFormats are the audio formats local playback can handle. opus is
// intentionally excluded: it is only ever sent over the Telegram/relay path,
// never to local Play, and the platform players wired here cannot decode it.
var supportedFormats = map[string]bool{
	"mp3": true,
	"wav": true,
}

func extForFormat(format string) string {
	if format == "wav" {
		return ".wav"
	}
	return ".mp3"
}

// buildPlayCommand returns the platform command to play a file of the given
// format. Separated out so it is unit-testable without real playback.
func buildPlayCommand(goos, format, path string) (*exec.Cmd, error) {
	switch goos {
	case "darwin":
		return exec.Command("afplay", path), nil
	case "linux":
		if _, err := exec.LookPath("mpv"); err == nil {
			return exec.Command("mpv", "--no-video", path), nil
		}
		if _, err := exec.LookPath("ffplay"); err == nil {
			return exec.Command("ffplay", "-nodisp", "-autoexit", path), nil
		}
		if format == "wav" {
			if _, err := exec.LookPath("aplay"); err == nil {
				return exec.Command("aplay", "-q", path), nil
			}
			return nil, fmt.Errorf("no suitable audio player found on Linux for wav (install mpv, ffplay, or aplay)")
		}
		if _, err := exec.LookPath("mpg123"); err == nil {
			return exec.Command("mpg123", "-q", path), nil
		}
		return nil, fmt.Errorf("no suitable audio player found on Linux for mp3 (install mpv, ffplay, or mpg123)")
	case "windows":
		// Escape single quotes for PowerShell single-quoted strings (' -> '')
		// so a temp path containing an apostrophe (e.g. user "O'Brien") is safe.
		q := strings.ReplaceAll(path, "'", "''")
		if format == "wav" {
			// SoundPlayer plays WAV natively.
			return exec.Command("powershell", "-NoProfile", "-Command",
				fmt.Sprintf("(New-Object Media.SoundPlayer '%s').PlaySync()", q)), nil
		}
		// MediaPlayer (WPF) handles MP3, which SoundPlayer cannot. The wait for
		// NaturalDuration is BOUNDED so an unloadable/invalid file can never
		// hang playback. The bound is generous (up to ~30s of 50ms polls) so a
		// large/slow-to-load-but-VALID MP3 is not truncated. If the duration
		// still never reports, the script writes an error and exits non-zero so
		// the Go side can tell that playback did not actually complete (rather
		// than the previous flat 200ms sleep that silently returned success).
		ps := fmt.Sprintf(
			"Add-Type -AssemblyName PresentationCore; "+
				"$p = New-Object System.Windows.Media.MediaPlayer; "+
				"$p.Open([uri]'%s'); $p.Play(); "+
				"$n = 0; while (-not $p.NaturalDuration.HasTimeSpan -and $n -lt 600) { Start-Sleep -Milliseconds 50; $n++ }; "+
				"if ($p.NaturalDuration.HasTimeSpan) { Start-Sleep -Seconds ([int][math]::Ceiling($p.NaturalDuration.TimeSpan.TotalSeconds) + 1); $p.Close() } "+
				"else { $p.Close(); Write-Error 'audio duration never reported; playback may be incomplete'; exit 1 }",
			q)
		return exec.Command("powershell", "-NoProfile", "-Command", ps), nil
	default:
		return nil, fmt.Errorf("unsupported platform: %s", goos)
	}
}

// Play writes audioData to a temp file of the given format and plays it.
// Only one audio plays at a time (serialized by playMu). format must be one of
// the supported local formats ("mp3" or "wav"); anything else (e.g. "opus")
// returns an error rather than being silently coerced to MP3.
func (p *Player) Play(audioData []byte, format string) error {
	if !supportedFormats[format] {
		return fmt.Errorf("unsupported audio format %q (supported: mp3, wav)", format)
	}

	// Serialize playback. Held for the whole sequence, including the blocking
	// run below.
	p.playMu.Lock()
	defer p.playMu.Unlock()

	// Status flag: set true just before Run, reset false after. Guarded by an
	// atomic so IsPlaying() observes the in-flight state without waiting on
	// playMu.
	p.isPlaying.Store(true)
	defer p.isPlaying.Store(false)

	tmpFile, err := os.CreateTemp("", "tts-*"+extForFormat(format))
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	defer os.Remove(tmpFile.Name()) //nolint:errcheck

	if _, err := tmpFile.Write(audioData); err != nil {
		tmpFile.Close() //nolint:errcheck
		return fmt.Errorf("failed to write audio data: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("failed to flush audio data: %w", err)
	}

	cmd, err := p.build(runtime.GOOS, format, tmpFile.Name())
	if err != nil {
		return err
	}
	if err := p.run(cmd); err != nil {
		return fmt.Errorf("audio playback failed: %w", err)
	}
	return nil
}

// IsPlaying returns whether audio is currently playing. It reads a lock-free
// atomic flag and returns immediately, even while a long playback holds playMu.
func (p *Player) IsPlaying() bool {
	return p.isPlaying.Load()
}
