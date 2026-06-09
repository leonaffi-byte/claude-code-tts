package server

import (
	"context"
	"errors"
	"strings"
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
	lastCap    string
	err        error
}

func (t *fakeTelegram) Send(ctx context.Context, audio []byte, format, caption string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.calls++
	t.lastFormat = format
	t.lastCap = caption
	return t.err
}
func (t *fakeTelegram) count() int { t.mu.Lock(); defer t.mu.Unlock(); return t.calls }
func (t *fakeTelegram) format() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lastFormat
}
func (t *fakeTelegram) lastCaption() string { t.mu.Lock(); defer t.mu.Unlock(); return t.lastCap }
func (t *fakeTelegram) captionHas(s string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Contains(t.lastCap, s)
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
	wp.Submit(SpeakRequest{Text: "hi", Profile: "default"})
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
	wp.Submit(SpeakRequest{Text: "hi", Profile: "default"})
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
	wp.Submit(SpeakRequest{Text: "hi", Profile: "default"})
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
	wp.Submit(SpeakRequest{Text: "hi", Profile: "default"})
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
	wp.Submit(SpeakRequest{Text: "hi", Profile: "default"})
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
	wp.Submit(SpeakRequest{Text: "hi", Profile: "default"})
	waitFor(t, func() bool { return wp.GetStatus().TotalFailed == 1 }, "phone-only job failed")
}

func TestWorkerPool_Mode_Phone_NotConfiguredFails(t *testing.T) {
	wp, _, _ := newModedPool(voicemode.Phone, "mp3", nil, "set $TELEGRAM_BOT_TOKEN")
	wp.Start()
	defer wp.Stop()
	wp.Submit(SpeakRequest{Text: "hi", Profile: "default"})
	waitFor(t, func() bool { return wp.GetStatus().TotalFailed == 1 }, "failed: telegram not configured")
}

func TestWorkerPool_Mode_Phone_NonMP3Fails(t *testing.T) {
	wp, _, _ := newModedPool(voicemode.Phone, "wav", &fakeTelegram{}, "")
	wp.Start()
	defer wp.Stop()
	wp.Submit(SpeakRequest{Text: "hi", Profile: "default"})
	waitFor(t, func() bool { return wp.GetStatus().TotalFailed == 1 }, "failed: telegram needs mp3")
}

type fakeSettings struct{ s voicemode.Settings }

func (f fakeSettings) Get() voicemode.Settings { return f.s }

func TestWorkerPool_AppliesSettingsOverride(t *testing.T) {
	tg := &fakeTelegram{}
	prov := &fakeProvider{format: "mp3"}
	player := &fakePlayer{}
	wp := NewWorkerPool(fakeResolver{prov: prov}, player, 1, 4).
		WithMode(fakeMode{m: voicemode.Both}).
		WithTelegram(tg, "").
		WithSettings(fakeSettings{s: voicemode.Settings{Voice: "onyx", Model: "tts-1-hd"}})
	wp.Start()
	defer wp.Stop()
	// job leaves voice/model empty -> settings override applies.
	wp.Submit(SpeakRequest{Text: "hi", Profile: "default"})
	waitFor(t, func() bool { return tg.count() == 1 }, "sent")
	// The fakeResolver returns Voice:"v"; the override must replace it with onyx.
	if gotV := tg.captionHas("onyx"); !gotV {
		t.Errorf("telegram caption %q missing overridden voice onyx", tg.lastCaption())
	}
}

func TestWorkerPool_ExplicitVoiceBeatsSettings(t *testing.T) {
	tg := &fakeTelegram{}
	prov := &fakeProvider{format: "mp3"}
	wp := NewWorkerPool(fakeResolver{prov: prov}, &fakePlayer{}, 1, 4).
		WithMode(fakeMode{m: voicemode.Phone}).
		WithTelegram(tg, "").
		WithSettings(fakeSettings{s: voicemode.Settings{Voice: "onyx"}})
	wp.Start()
	defer wp.Stop()
	wp.Submit(SpeakRequest{Text: "hi", Provider: "fake", Voice: "echo"}) // explicit voice
	waitFor(t, func() bool { return tg.count() == 1 }, "sent")
	if !tg.captionHas("echo") {
		t.Errorf("caption %q should keep explicit voice echo", tg.lastCaption())
	}
}
