package server

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ybouhjira/claude-code-tts/internal/tts"
	"github.com/ybouhjira/claude-code-tts/internal/voicemode"
)

// --- shared test doubles (also used by server_test.go) ---

type fakeProvider struct {
	format string
	calls  atomic.Int32
}

func (f *fakeProvider) Name() string          { return "fake" }
func (f *fakeProvider) Voices() []string      { return nil }
func (f *fakeProvider) DefaultFormat() string { return f.format }
func (f *fakeProvider) Synthesize(ctx context.Context, req tts.Request) (tts.Audio, error) {
	f.calls.Add(1)
	format := f.format
	if req.Format != "" {
		format = req.Format // honor an explicit format request (e.g. "opus")
	}
	return tts.Audio{Data: []byte("AUDIO"), Format: format}, nil
}

type fakeResolver struct {
	prov tts.Provider
	err  error
}

func (r fakeResolver) Resolve(profile string) (tts.Provider, tts.Request, error) {
	if r.err != nil {
		return nil, tts.Request{}, r.err
	}
	if profile == "" {
		return nil, tts.Request{}, errors.New("empty profile")
	}
	return r.prov, tts.Request{Voice: "v"}, nil
}
func (r fakeResolver) ResolveVoice(provider, voice string, speed float64) (tts.Provider, tts.Request, error) {
	if r.err != nil {
		return nil, tts.Request{}, r.err
	}
	return r.prov, tts.Request{Voice: voice, Speed: speed}, nil
}
func (r fakeResolver) Default() (tts.Provider, tts.Request, error) { return r.Resolve("default") }

func okResolver(format string) fakeResolver { return fakeResolver{prov: &fakeProvider{format: format}} }

type fakePlayer struct {
	mu         sync.Mutex
	calls      int
	lastData   []byte
	lastFormat string
}

func (p *fakePlayer) Play(data []byte, format string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	p.lastData, p.lastFormat = data, format
	return nil
}
func (p *fakePlayer) IsPlaying() bool { return false }
func (p *fakePlayer) snapshot() (int, []byte, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls, p.lastData, p.lastFormat
}

func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", msg)
}

// --- tests ---

func TestNewWorkerPool(t *testing.T) {
	wp := NewWorkerPool(okResolver("mp3"), &fakePlayer{}, 2, 10)
	if wp.resolver == nil || wp.player == nil {
		t.Fatal("resolver/player not set")
	}
	if wp.workerCount != 2 || wp.queueSize != 10 {
		t.Errorf("got %d/%d", wp.workerCount, wp.queueSize)
	}
}

func TestWorkerPool_ProcessesJobWithFormat(t *testing.T) {
	player := &fakePlayer{}
	wp := NewWorkerPool(okResolver("wav"), player, 1, 4)
	wp.Start()
	defer wp.Stop()

	if _, err := wp.Submit(SpeakRequest{Text: "hello", Profile: "default"}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	waitFor(t, func() bool { n, _, _ := player.snapshot(); return n == 1 }, "player called")
	_, data, format := player.snapshot()
	if string(data) != "AUDIO" || format != "wav" {
		t.Errorf("player got %q/%q, want AUDIO/wav", data, format)
	}
}

func TestWorkerPool_SubmitQueueFull(t *testing.T) {
	// No Start(): nothing drains, so the queue fills at capacity.
	wp := NewWorkerPool(okResolver("mp3"), &fakePlayer{}, 1, 2)
	if _, err := wp.Submit(SpeakRequest{Text: "1", Profile: "default"}); err != nil {
		t.Fatalf("submit 1: %v", err)
	}
	if _, err := wp.Submit(SpeakRequest{Text: "2", Profile: "default"}); err != nil {
		t.Fatalf("submit 2: %v", err)
	}
	if _, err := wp.Submit(SpeakRequest{Text: "3", Profile: "default"}); err == nil {
		t.Fatal("expected queue-full error on third submit")
	}
}

func TestWorkerPool_FailedJobOnResolveError(t *testing.T) {
	wp := NewWorkerPool(fakeResolver{err: errors.New("boom")}, &fakePlayer{}, 1, 4)
	wp.Start()
	defer wp.Stop()
	if _, err := wp.Submit(SpeakRequest{Text: "hi", Profile: "default"}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	waitFor(t, func() bool { return wp.GetStatus().TotalFailed == 1 }, "job failed")
}

func TestWorkerPool_JobHistoryLimit(t *testing.T) {
	wp := NewWorkerPool(okResolver("mp3"), &fakePlayer{}, 1, 200)
	for i := 0; i < 150; i++ {
		_, _ = wp.Submit(SpeakRequest{Text: "t", Profile: "default"})
	}
	if got := len(wp.GetStatus().RecentJobs); got > 10 {
		t.Errorf("recent jobs = %d, want <= 10", got)
	}
	// The full job history is capped at 100 regardless of how many were submitted.
	wp.historyMu.RLock()
	got := len(wp.jobHistory)
	wp.historyMu.RUnlock()
	if got > 100 {
		t.Errorf("jobHistory = %d, want <= 100", got)
	}
}

func TestWorkerPool_NoProfileUsesDefault(t *testing.T) {
	player := &fakePlayer{}
	wp := NewWorkerPool(okResolver("mp3"), player, 1, 4)
	wp.Start()
	defer wp.Stop()
	// With neither profile nor provider set, the worker must resolve via
	// Default() (env-aware). fakeResolver.Resolve("") errors, so a regression
	// that called Resolve(job.Profile) here would fail the job instead.
	if _, err := wp.Submit(SpeakRequest{Text: "hi"}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	waitFor(t, func() bool { n, _, _ := player.snapshot(); return n == 1 }, "played via Default()")
}

func TestWorkerPool_StartStop(t *testing.T) {
	wp := NewWorkerPool(okResolver("mp3"), &fakePlayer{}, 2, 10)
	wp.Start()
	wp.Stop() // must not panic or deadlock
}

// --- mode-aware delivery tests ---

type fakeMode struct{ m voicemode.Mode }

func (f fakeMode) Get() voicemode.Mode { return f.m }

type fakeTelegram struct {
	mu         sync.Mutex
	calls      int
	lastFormat string
	err        error
}

func (t *fakeTelegram) Send(ctx context.Context, audio []byte, format, caption string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.calls++
	t.lastFormat = format
	return t.err
}
func (t *fakeTelegram) count() int { t.mu.Lock(); defer t.mu.Unlock(); return t.calls }
func (t *fakeTelegram) format() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lastFormat
}

func newModedPool(mode voicemode.Mode, format string, tg *fakeTelegram, tgReason string) (*WorkerPool, *fakePlayer, *fakeProvider) {
	prov := &fakeProvider{format: format}
	player := &fakePlayer{}
	wp := NewWorkerPool(fakeResolver{prov: prov}, player, 1, 4).WithMode(fakeMode{m: mode})
	if tg != nil {
		wp.WithTelegram(tg, tgReason)
	} else {
		// Pass a literal nil so wp.telegram is a truly-nil interface, not a
		// typed-nil (*fakeTelegram)(nil) that would slip past the nil check.
		wp.WithTelegram(nil, tgReason)
	}
	return wp, player, prov
}

func TestWorkerPool_Mode_Off_SkipsEverything(t *testing.T) {
	wp, player, prov := newModedPool(voicemode.Off, "mp3", &fakeTelegram{}, "")
	wp.Start()
	defer wp.Stop()
	_, _ = wp.Submit(SpeakRequest{Text: "hi", Profile: "default"})
	waitFor(t, func() bool { return wp.GetStatus().TotalProcessed == 1 }, "job completed")
	if n, _, _ := player.snapshot(); n != 0 {
		t.Errorf("player called %d times in off mode, want 0", n)
	}
	if prov.calls.Load() != 0 {
		t.Errorf("synth called %d times in off mode, want 0 (no cost)", prov.calls.Load())
	}
}

func TestWorkerPool_Mode_Computer_LocalOnly(t *testing.T) {
	tg := &fakeTelegram{}
	wp, player, _ := newModedPool(voicemode.Computer, "mp3", tg, "")
	wp.Start()
	defer wp.Stop()
	_, _ = wp.Submit(SpeakRequest{Text: "hi", Profile: "default"})
	waitFor(t, func() bool { n, _, _ := player.snapshot(); return n == 1 }, "played locally")
	if tg.count() != 0 {
		t.Errorf("telegram called in computer mode")
	}
}

func TestWorkerPool_Mode_Phone_TelegramOnly(t *testing.T) {
	tg := &fakeTelegram{}
	wp, player, _ := newModedPool(voicemode.Phone, "mp3", tg, "")
	wp.Start()
	defer wp.Stop()
	_, _ = wp.Submit(SpeakRequest{Text: "hi", Profile: "default"})
	waitFor(t, func() bool { return tg.count() == 1 }, "sent to telegram")
	if n, _, _ := player.snapshot(); n != 0 {
		t.Errorf("player called in phone mode")
	}
	if f := tg.format(); f != "opus" {
		t.Errorf("telegram format = %q, want opus (voice message)", f)
	}
}

func TestWorkerPool_Mode_Both(t *testing.T) {
	tg := &fakeTelegram{}
	wp, player, _ := newModedPool(voicemode.Both, "mp3", tg, "")
	wp.Start()
	defer wp.Stop()
	_, _ = wp.Submit(SpeakRequest{Text: "hi", Profile: "default"})
	waitFor(t, func() bool { n, _, _ := player.snapshot(); return n == 1 && tg.count() == 1 }, "played + sent")
	if f := wp.GetStatus().TotalFailed; f != 0 {
		t.Errorf("both-mode job should succeed, TotalFailed=%d", f)
	}
}

func TestWorkerPool_Mode_Both_TelegramErrorStillPlaysLocal(t *testing.T) {
	tg := &fakeTelegram{err: errors.New("telegram down")}
	wp, player, _ := newModedPool(voicemode.Both, "mp3", tg, "")
	wp.Start()
	defer wp.Stop()
	_, _ = wp.Submit(SpeakRequest{Text: "hi", Profile: "default"})
	waitFor(t, func() bool { n, _, _ := player.snapshot(); return n == 1 }, "played locally despite telegram error")
	if wp.GetStatus().TotalFailed != 0 {
		t.Errorf("both-mode job should not fail when local playback works")
	}
}

func TestWorkerPool_Mode_Phone_TelegramErrorFails(t *testing.T) {
	tg := &fakeTelegram{err: errors.New("telegram down")}
	wp, _, _ := newModedPool(voicemode.Phone, "mp3", tg, "")
	wp.Start()
	defer wp.Stop()
	_, _ = wp.Submit(SpeakRequest{Text: "hi", Profile: "default"})
	waitFor(t, func() bool { return wp.GetStatus().TotalFailed == 1 }, "phone-only job failed")
}

func TestWorkerPool_Mode_Phone_NotConfiguredFails(t *testing.T) {
	wp, _, _ := newModedPool(voicemode.Phone, "mp3", nil, "set $TELEGRAM_BOT_TOKEN")
	// A pool with no sender wired must report telegram as not configured.
	if wp.GetStatus().TelegramConfigured {
		t.Fatal("TelegramConfigured = true, want false when no sender is wired")
	}
	wp.Start()
	defer wp.Stop()
	_, _ = wp.Submit(SpeakRequest{Text: "hi", Profile: "default"})
	waitFor(t, func() bool { return wp.GetStatus().TotalFailed == 1 }, "failed: telegram not configured")
}

// TestWorkerPool_WithTelegram_TypedNilNormalized guards the typed-nil interface
// trap: a (*fakeTelegram)(nil) wrapped in the telegramSender interface must be
// normalized to a real nil so the "telegram not configured" guarantee holds and
// a Phone-mode job fails fast instead of dereferencing a nil sender.
func TestWorkerPool_WithTelegram_TypedNilNormalized(t *testing.T) {
	var typedNil *fakeTelegram // nil pointer, non-nil interface when passed directly
	prov := &fakeProvider{format: "mp3"}
	wp := NewWorkerPool(fakeResolver{prov: prov}, &fakePlayer{}, 1, 4).
		WithMode(fakeMode{m: voicemode.Phone}).
		WithTelegram(typedNil, "typed-nil sender")

	if wp.GetStatus().TelegramConfigured {
		t.Fatal("TelegramConfigured = true for a typed-nil sender; normalization failed")
	}

	wp.Start()
	defer wp.Stop()
	_, _ = wp.Submit(SpeakRequest{Text: "hi", Profile: "default"})
	waitFor(t, func() bool { return wp.GetStatus().TotalFailed == 1 }, "failed: typed-nil telegram treated as not configured")
}

func TestWorkerPool_Mode_Phone_NonMP3Fails(t *testing.T) {
	wp, _, _ := newModedPool(voicemode.Phone, "wav", &fakeTelegram{}, "")
	wp.Start()
	defer wp.Stop()
	_, _ = wp.Submit(SpeakRequest{Text: "hi", Profile: "default"})
	waitFor(t, func() bool { return wp.GetStatus().TotalFailed == 1 }, "failed: telegram needs mp3")
}

// --- shutdown / concurrency tests ---

// TestWorkerPool_StopIsIdempotent verifies repeated Stop() calls (and a Stop
// after Server.Shutdown) do not panic with "close of closed channel".
func TestWorkerPool_StopIsIdempotent(t *testing.T) {
	wp := NewWorkerPool(okResolver("mp3"), &fakePlayer{}, 2, 10)
	wp.Start()
	wp.Stop()
	wp.Stop() // second call must be a safe no-op
	wp.Stop() // and a third, for good measure
}

// TestWorkerPool_SubmitAfterStopReturnsError verifies Submit after shutdown
// returns a clean error instead of panicking with "send on closed channel".
func TestWorkerPool_SubmitAfterStopReturnsError(t *testing.T) {
	wp := NewWorkerPool(okResolver("mp3"), &fakePlayer{}, 2, 10)
	wp.Start()
	wp.Stop()

	job, err := wp.Submit(SpeakRequest{Text: "late", Profile: "default"})
	if err == nil {
		t.Fatal("expected error submitting after Stop, got nil")
	}
	if job == nil || job.Status != "failed" {
		t.Fatalf("expected failed job, got %+v", job)
	}
}

// TestWorkerPool_ConcurrentSubmitDuringStop hammers Submit from many goroutines
// while Stop() runs concurrently. It must never panic on a closed channel; every
// Submit must either succeed or return a clean error. Run with -race.
func TestWorkerPool_ConcurrentSubmitDuringStop(t *testing.T) {
	wp := NewWorkerPool(okResolver("mp3"), &fakePlayer{}, 2, 100)
	wp.Start()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// A panic here (send on closed channel) would crash the test process.
			_, _ = wp.Submit(SpeakRequest{Text: "x", Profile: "default"})
		}()
	}
	wp.Stop()
	wg.Wait()
}

// TestWorkerPool_StopDrainsQueuedJobs verifies graceful shutdown actually drains
// already-accepted jobs rather than dropping them. Jobs are buffered while the
// pool is paused (workers gate on pause before dequeuing), then on resume+Stop
// every buffered job must be processed.
func TestWorkerPool_StopDrainsQueuedJobs(t *testing.T) {
	player := &fakePlayer{}
	wp := NewWorkerPool(okResolver("mp3"), player, 1, 20)
	wp.Pause() // workers park before dequeuing, so submits buffer in the channel
	wp.Start()

	const n = 10
	for i := 0; i < n; i++ {
		if _, err := wp.Submit(SpeakRequest{Text: "t", Profile: "default"}); err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
	}
	// Nothing processed yet because the pool is paused.
	if got := wp.GetStatus().TotalProcessed; got != 0 {
		t.Fatalf("processed %d while paused, want 0", got)
	}

	wp.Resume()
	// Stop must drain all buffered jobs before returning. waitFor first to avoid
	// the narrow pause-loop/shutdown race (a worker still parked in the 100ms
	// pause sleep when Stop closes shutdown would exit early by design); once
	// unpaused workers are draining, Stop blocks on wg.Wait until they finish.
	waitFor(t, func() bool { return wp.GetStatus().TotalProcessed == n }, "all buffered jobs drained")
	wp.Stop()

	if got := wp.GetStatus().TotalProcessed; got != n {
		t.Errorf("processed %d after drain, want %d (jobs were dropped)", got, n)
	}
	if calls, _, _ := player.snapshot(); calls != n {
		t.Errorf("player called %d times, want %d", calls, n)
	}
}

// blockingPlayer blocks the first Play call until released, so a worker can be
// held mid-job while more jobs buffer in the queue.
type blockingPlayer struct {
	mu      sync.Mutex
	calls   int
	gate    chan struct{}
	entered chan struct{}
	once    sync.Once
}

func newBlockingPlayer() *blockingPlayer {
	return &blockingPlayer{gate: make(chan struct{}), entered: make(chan struct{})}
}
func (p *blockingPlayer) Play(_ []byte, _ string) error {
	p.once.Do(func() { close(p.entered); <-p.gate })
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	return nil
}
func (p *blockingPlayer) IsPlaying() bool { return false }
func (p *blockingPlayer) count() int      { p.mu.Lock(); defer p.mu.Unlock(); return p.calls }

// TestWorkerPool_StopDrainsWhileWorkerBusy verifies that Stop() called while a
// worker is mid-job still drains the jobs that buffered behind it, rather than
// the worker taking a shutdown branch and abandoning the queue.
func TestWorkerPool_StopDrainsWhileWorkerBusy(t *testing.T) {
	player := newBlockingPlayer()
	wp := NewWorkerPool(okResolver("mp3"), player, 1, 20)
	wp.Start()

	const n = 6
	for i := 0; i < n; i++ {
		if _, err := wp.Submit(SpeakRequest{Text: "t", Profile: "default"}); err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
	}
	<-player.entered // the single worker is now blocked inside Play on job 0

	// Stop concurrently while the worker is busy and 5 jobs are buffered.
	done := make(chan struct{})
	go func() { wp.Stop(); close(done) }()

	close(player.gate) // release the worker; it must now drain the remaining jobs

	<-done
	if got := player.count(); got != n {
		t.Errorf("player processed %d jobs, want %d (buffered jobs dropped on Stop)", got, n)
	}
	if got := wp.GetStatus().TotalProcessed; got != n {
		t.Errorf("TotalProcessed = %d, want %d", got, n)
	}
}

// TestWorkerPool_PausedLeavesJobsInQueue verifies the pause-before-dequeue gate:
// a paused pool leaves submitted jobs in the channel where QueuePending and
// Clear() can see them, rather than a worker parking with a job already pulled.
func TestWorkerPool_PausedLeavesJobsInQueue(t *testing.T) {
	wp := NewWorkerPool(okResolver("mp3"), &fakePlayer{}, 2, 20)
	wp.Pause()
	wp.Start()

	const n = 5
	for i := 0; i < n; i++ {
		if _, err := wp.Submit(SpeakRequest{Text: "t", Profile: "default"}); err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
	}
	// Give the parked workers a chance to (incorrectly) dequeue if the gate were
	// after the receive; the count must stay at n.
	waitFor(t, func() bool { return wp.GetStatus().QueuePending == n }, "all jobs visible as pending while paused")

	cleared := wp.Clear()
	if cleared != n {
		t.Errorf("Clear() reclaimed %d jobs, want %d (paused jobs not reclaimable)", cleared, n)
	}
	if got := wp.GetStatus().QueuePending; got != 0 {
		t.Errorf("QueuePending = %d after Clear, want 0", got)
	}

	wp.Resume()
	wp.Stop()
}
