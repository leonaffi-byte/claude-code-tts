package audio

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
)

// Player handles audio playback with mutex protection.
type Player struct {
	mu        sync.Mutex
	isPlaying bool
}

// NewPlayer creates a new audio player.
func NewPlayer() *Player { return &Player{} }

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
		} else {
			if _, err := exec.LookPath("mpg123"); err == nil {
				return exec.Command("mpg123", "-q", path), nil
			}
		}
		return nil, fmt.Errorf("no suitable audio player found on Linux (install mpv, ffplay, mpg123, or aplay)")
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
		// NaturalDuration is BOUNDED (max ~3s) so an unloadable/invalid file can
		// never hang playback — without the bound, a file that never reports a
		// duration would spin forever.
		ps := fmt.Sprintf(
			"Add-Type -AssemblyName PresentationCore; "+
				"$p = New-Object System.Windows.Media.MediaPlayer; "+
				"$p.Open([uri]'%s'); $p.Play(); "+
				"$n = 0; while (-not $p.NaturalDuration.HasTimeSpan -and $n -lt 60) { Start-Sleep -Milliseconds 50; $n++ }; "+
				"if ($p.NaturalDuration.HasTimeSpan) { Start-Sleep -Seconds ([int][math]::Ceiling($p.NaturalDuration.TimeSpan.TotalSeconds) + 1) } else { Start-Sleep -Milliseconds 200 }; "+
				"$p.Close()",
			q)
		return exec.Command("powershell", "-NoProfile", "-Command", ps), nil
	default:
		return nil, fmt.Errorf("unsupported platform: %s", goos)
	}
}

// Play writes audioData to a temp file of the given format and plays it.
// Only one audio plays at a time (mutex protected). format is "mp3" or "wav".
func (p *Player) Play(audioData []byte, format string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.isPlaying = true
	defer func() { p.isPlaying = false }()

	tmpFile, err := os.CreateTemp("", "tts-*"+extForFormat(format))
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.Write(audioData); err != nil {
		tmpFile.Close()
		return fmt.Errorf("failed to write audio data: %w", err)
	}
	tmpFile.Close()

	cmd, err := buildPlayCommand(runtime.GOOS, format, tmpFile.Name())
	if err != nil {
		return err
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("audio playback failed: %w", err)
	}
	return nil
}

// IsPlaying returns whether audio is currently playing.
func (p *Player) IsPlaying() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.isPlaying
}
