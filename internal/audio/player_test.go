package audio

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// fakeRunner is a hermetic stand-in for the real command runner. It records
// the commands it was asked to run, returns a canned error, and tracks the
// maximum number of concurrent in-flight runs so tests can assert that Play
// serializes access.
type fakeRunner struct {
	mu            sync.Mutex
	cmds          []*exec.Cmd
	err           error // returned from run
	calls         int
	inFlight      int
	maxInFlight   int
	beforeReturn  func() // optional hook invoked while "in flight"
	observedPaths []string
}

func (f *fakeRunner) run(cmd *exec.Cmd) error {
	f.mu.Lock()
	f.calls++
	f.cmds = append(f.cmds, cmd)
	if len(cmd.Args) > 0 {
		f.observedPaths = append(f.observedPaths, cmd.Args[len(cmd.Args)-1])
	}
	f.inFlight++
	if f.inFlight > f.maxInFlight {
		f.maxInFlight = f.inFlight
	}
	hook := f.beforeReturn
	f.mu.Unlock()

	if hook != nil {
		hook()
	}

	f.mu.Lock()
	f.inFlight--
	err := f.err
	f.mu.Unlock()
	return err
}

// newFakePlayer returns a Player whose build/run seams are replaced by a fake
// runner, so Play() never spawns a real OS audio process. The build seam
// records the path it was handed (delegating ext/path decisions to Play) and
// returns a trivial command; the fake runner records and returns canned err.
func newFakePlayer(runErr error) (*Player, *fakeRunner) {
	fr := &fakeRunner{err: runErr}
	p := &Player{
		build: func(goos, format, path string) (*exec.Cmd, error) {
			// A harmless command carrying the temp path as its final arg, so the
			// runner can observe the path Play created.
			return exec.Command("fake-player", path), nil
		},
		run: fr.run,
	}
	return p, fr
}

func TestBuildPlayCommand(t *testing.T) {
	cases := []struct {
		goos, format string
		wantContains string // substring expected somewhere in name+args
		wantErr      bool
	}{
		{"windows", "wav", "SoundPlayer", false},
		{"windows", "mp3", "MediaPlayer", false},
		{"darwin", "mp3", "afplay", false},
		{"darwin", "wav", "afplay", false},
		{"plan9", "mp3", "", true},
	}
	for _, c := range cases {
		t.Run(c.goos+"_"+c.format, func(t *testing.T) {
			cmd, err := buildPlayCommand(c.goos, c.format, "/tmp/x."+c.format)
			if c.wantErr {
				if err == nil {
					t.Fatalf("want error for %s/%s", c.goos, c.format)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			joined := cmd.Path + " " + strings.Join(cmd.Args, " ")
			if !strings.Contains(joined, c.wantContains) {
				t.Errorf("cmd %q missing %q", joined, c.wantContains)
			}
		})
	}
}

func TestExtForFormat(t *testing.T) {
	if extForFormat("wav") != ".wav" || extForFormat("mp3") != ".mp3" || extForFormat("") != ".mp3" {
		t.Error("extForFormat mapping wrong")
	}
}

func TestNewPlayer(t *testing.T) {
	player := NewPlayer()

	if player == nil {
		t.Fatal("expected player to be created")
	}
	if player.IsPlaying() {
		t.Error("expected isPlaying to be false initially")
	}
	if player.build == nil || player.run == nil {
		t.Error("expected build/run seams to be wired by NewPlayer")
	}
}

func TestPlayer_IsPlaying_Initial(t *testing.T) {
	player := NewPlayer()

	if player.IsPlaying() {
		t.Error("expected IsPlaying() to return false initially")
	}
}

func TestPlayer_IsPlaying_ThreadSafe(t *testing.T) {
	player := NewPlayer()

	var wg sync.WaitGroup
	results := make([]bool, 100)

	// Call IsPlaying concurrently
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = player.IsPlaying()
		}(i)
	}

	wg.Wait()

	// All results should be false (no audio playing)
	for i, result := range results {
		if result {
			t.Errorf("expected result[%d] to be false", i)
		}
	}
}

// TestPlayer_Play_Success asserts the happy path is hermetic and coherent: the
// injected runner returns nil, Play returns nil, IsPlaying() is false
// afterward, the runner saw the temp path with the correct extension, and the
// temp file is removed before Play returns.
func TestPlayer_Play_Success(t *testing.T) {
	cases := []struct {
		format  string
		wantExt string
	}{
		{"wav", ".wav"},
		{"mp3", ".mp3"},
	}
	for _, c := range cases {
		t.Run(c.format, func(t *testing.T) {
			player, fr := newFakePlayer(nil)

			// Capture the temp path the runner was handed so we can stat it.
			var seenPath atomic.Value
			fr.beforeReturn = func() {
				fr.mu.Lock()
				if len(fr.observedPaths) > 0 {
					seenPath.Store(fr.observedPaths[len(fr.observedPaths)-1])
				}
				fr.mu.Unlock()
				// While "playing", IsPlaying() must report true.
				if !player.IsPlaying() {
					t.Error("expected IsPlaying() true while playback is in flight")
				}
			}

			if err := player.Play([]byte("audio-bytes"), c.format); err != nil {
				t.Fatalf("Play returned error: %v", err)
			}

			if player.IsPlaying() {
				t.Error("expected IsPlaying() false after Play returned")
			}
			if fr.calls != 1 {
				t.Errorf("expected runner called once, got %d", fr.calls)
			}

			path, _ := seenPath.Load().(string)
			if path == "" {
				t.Fatal("runner did not observe a temp path")
			}
			if !strings.HasSuffix(path, c.wantExt) {
				t.Errorf("temp path %q does not have extension %q", path, c.wantExt)
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("temp file %q should be removed after Play; stat err = %v", path, err)
			}
		})
	}
}

// TestPlayer_Play_RunnerError asserts a runner failure is wrapped and surfaced,
// and isPlaying is reset.
func TestPlayer_Play_RunnerError(t *testing.T) {
	player, fr := newFakePlayer(errors.New("boom: unsupported codec"))

	err := player.Play([]byte("data"), "wav")
	if err == nil {
		t.Fatal("expected error from failing runner")
	}
	if !strings.Contains(err.Error(), "audio playback failed") {
		t.Errorf("error %q should be wrapped with 'audio playback failed'", err)
	}
	if !strings.Contains(err.Error(), "unsupported codec") {
		t.Errorf("error %q should carry the underlying runner diagnostic", err)
	}
	if player.IsPlaying() {
		t.Error("expected IsPlaying() false after failed play")
	}
	if fr.calls != 1 {
		t.Errorf("expected runner called once, got %d", fr.calls)
	}
}

// TestPlayer_Play_UnsupportedFormat asserts opus (and arbitrary strings) are
// rejected with a clear error and never reach the runner / temp-file logic.
func TestPlayer_Play_UnsupportedFormat(t *testing.T) {
	for _, format := range []string{"opus", "", "ogg", "garbage"} {
		t.Run("format_"+format, func(t *testing.T) {
			player, fr := newFakePlayer(nil)
			err := player.Play([]byte("data"), format)
			if err == nil {
				t.Fatalf("expected error for unsupported format %q", format)
			}
			if !strings.Contains(err.Error(), "unsupported audio format") {
				t.Errorf("error %q should mention unsupported audio format", err)
			}
			if fr.calls != 0 {
				t.Errorf("runner must not be invoked for unsupported format, got %d calls", fr.calls)
			}
			if player.IsPlaying() {
				t.Error("IsPlaying() should be false after rejected format")
			}
		})
	}
}

func TestPlayer_PlaySetsIsPlaying(t *testing.T) {
	player := NewPlayer()

	if player.IsPlaying() {
		t.Error("isPlaying should be false before Play()")
	}
}

// TestPlayer_ConcurrentPlayAttempts fans out 10 goroutines through an injected
// fake runner (no real subprocesses) and asserts: every goroutine returns
// (bounded by WaitGroup), the runner was invoked exactly 10 times, and Play
// serialized them so at most one was ever in flight. A missing mutex unlock
// would show up as maxInFlight > 1.
func TestPlayer_ConcurrentPlayAttempts(t *testing.T) {
	player, fr := newFakePlayer(nil)

	const n = 10
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = player.Play([]byte("data"), "wav")
		}()
	}
	wg.Wait()

	if fr.calls != n {
		t.Errorf("expected %d runner calls, got %d", n, fr.calls)
	}
	if fr.maxInFlight != 1 {
		t.Errorf("expected playback serialized (maxInFlight==1), got %d", fr.maxInFlight)
	}
	if player.IsPlaying() {
		t.Error("IsPlaying() should be false after all plays complete")
	}
}

// Table-driven tests for NewPlayer
func TestNewPlayer_TableDriven(t *testing.T) {
	tests := []struct {
		name string
	}{
		{"first player"},
		{"second player"},
		{"third player"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			player := NewPlayer()
			if player == nil {
				t.Fatal("expected player to be created")
			}
			if player.IsPlaying() {
				t.Error("expected isPlaying to be false initially")
			}
		})
	}
}

func TestPlayer_IsPlaying_ConsistentState(t *testing.T) {
	player := NewPlayer()

	// Call IsPlaying multiple times, should always be consistent
	for i := 0; i < 10; i++ {
		if player.IsPlaying() {
			t.Errorf("iteration %d: expected IsPlaying() to return false", i)
		}
	}
}

// TestPlayer_Play_RemovesTempFile asserts the temp file is created and cleaned
// up. The fake runner captures the path so we can verify removal explicitly.
func TestPlayer_Play_RemovesTempFile(t *testing.T) {
	player, fr := newFakePlayer(nil)

	if err := player.Play([]byte("fake-audio-data"), "wav"); err != nil {
		t.Fatalf("Play returned error: %v", err)
	}

	if len(fr.observedPaths) != 1 {
		t.Fatalf("expected exactly one temp path, got %d", len(fr.observedPaths))
	}
	path := fr.observedPaths[0]
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("temp file %q should be removed after Play; stat err = %v", path, err)
	}
}

func TestPlayer_IsPlaying_AfterPlayFailure(t *testing.T) {
	player, _ := newFakePlayer(errors.New("invalid audio"))

	// Try to play data with a runner that fails.
	_ = player.Play([]byte("invalid"), "wav")

	// After failed play, isPlaying should be false
	if player.IsPlaying() {
		t.Error("expected IsPlaying() to be false after failed play")
	}
}

func TestPlayer_MutexProtectsIsPlaying(t *testing.T) {
	player := NewPlayer()

	// Concurrent reads of isPlaying should work safely
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// This should not race or panic
			_ = player.IsPlaying()
		}()
	}
	wg.Wait()
}

// TestPlayer_IsPlaying_DuringPlay asserts the core IsPlaying contract: a
// concurrent caller observes true WHILE playback is in flight and false after
// it completes. The injected runner blocks until the test releases it, so the
// in-flight window is deterministic and no real process is spawned.
func TestPlayer_IsPlaying_DuringPlay(t *testing.T) {
	player, fr := newFakePlayer(nil)

	release := make(chan struct{})
	entered := make(chan struct{})
	fr.beforeReturn = func() {
		close(entered)
		<-release
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = player.Play([]byte("data"), "wav")
	}()

	// Wait until playback is actually in flight.
	<-entered
	if !player.IsPlaying() {
		t.Error("expected IsPlaying() true while playback is in flight")
	}

	// Let playback finish.
	close(release)
	<-done

	if player.IsPlaying() {
		t.Error("expected IsPlaying() false after playback completes")
	}
}

func TestPlayer_Play_EmptyData(t *testing.T) {
	player, fr := newFakePlayer(nil)

	if err := player.Play([]byte{}, "wav"); err != nil {
		t.Fatalf("Play of empty data returned error: %v", err)
	}
	if fr.calls != 1 {
		t.Errorf("expected runner to be invoked for empty data, got %d", fr.calls)
	}
}

func TestPlayer_Play_LargeData(t *testing.T) {
	player, fr := newFakePlayer(nil)

	// Create 10MB of fake data; with the injected runner there is no real
	// playback, so this just exercises temp-file write of a large payload.
	largeData := make([]byte, 10*1024*1024)
	for i := range largeData {
		largeData[i] = byte(i % 256)
	}

	if err := player.Play(largeData, "wav"); err != nil {
		t.Fatalf("Play of large data returned error: %v", err)
	}
	if fr.calls != 1 {
		t.Errorf("expected runner called once, got %d", fr.calls)
	}
}

func TestPlayer_Play_NilData(t *testing.T) {
	player, fr := newFakePlayer(nil)

	if err := player.Play(nil, "wav"); err != nil {
		t.Fatalf("Play of nil data returned error: %v", err)
	}
	if fr.calls != 1 {
		t.Errorf("expected runner called once, got %d", fr.calls)
	}
}

func TestPlayer_Sequential_Plays(t *testing.T) {
	player, fr := newFakePlayer(nil)

	// Multiple sequential plays should work and reset isPlaying each time.
	for i := 0; i < 5; i++ {
		if err := player.Play([]byte("test-data"), "wav"); err != nil {
			t.Fatalf("iteration %d: Play returned error: %v", i, err)
		}
		if player.IsPlaying() {
			t.Errorf("iteration %d: expected IsPlaying() to be false after play completed", i)
		}
	}
	if fr.calls != 5 {
		t.Errorf("expected 5 runner calls, got %d", fr.calls)
	}
}

func TestPlayer_ConcurrentIsPlayingCalls(t *testing.T) {
	player := NewPlayer()

	// Many concurrent IsPlaying calls should not cause race conditions
	var wg sync.WaitGroup
	results := make([]bool, 1000)

	for i := 0; i < 1000; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = player.IsPlaying()
		}(i)
	}

	wg.Wait()

	// All results should be false (no audio playing)
	for i, result := range results {
		if result {
			t.Errorf("result[%d] should be false", i)
		}
	}
}

func TestPlayer_StructureInitialization(t *testing.T) {
	player := NewPlayer()

	// Verify the player struct is properly initialized
	if player == nil {
		t.Fatal("player should not be nil")
	}

	// isPlaying should be false
	if player.IsPlaying() {
		t.Error("isPlaying field should be false initially")
	}
}

// TestRunCommand_CapturesOutput verifies the default runner includes the
// command's combined output in the error when it fails. This is platform
// independent: it runs the Go test binary itself (always present) with an
// unknown flag, which prints to stderr and exits non-zero.
func TestRunCommand_CapturesOutput(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^$", "-definitely-not-a-real-flag")
	err := runCommand(cmd)
	if err == nil {
		t.Fatal("expected runCommand to return an error for a failing command")
	}
	// The flag-parse failure message should be surfaced (captured output).
	if !strings.Contains(err.Error(), "flag") && !strings.Contains(err.Error(), "definitely-not-a-real-flag") {
		t.Errorf("expected captured output in error, got: %v", err)
	}
}

// TestRealPlay_Integration is an opt-in integration test that exercises the
// real platform player. It is skipped in -short mode (the default for normal
// unit runs) so the hermetic suite never spawns OS audio processes.
func TestRealPlay_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real audio playback in short mode")
	}
	// A minimal (invalid) payload: the goal is only to exercise the real build
	// + run seams end to end. Most platform players will error on this, which
	// is acceptable for an opt-in smoke test; we assert only that Play does not
	// panic and resets state.
	player := NewPlayer()
	_ = player.Play([]byte("not-real-audio"), "wav")
	if player.IsPlaying() {
		t.Error("expected IsPlaying() false after real Play returned")
	}
}
