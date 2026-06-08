package server

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ybouhjira/claude-code-tts/internal/tts"
)

// --- shared test doubles (also used by server_test.go) ---

type fakeProvider struct{ format string }

func (f *fakeProvider) Name() string          { return "fake" }
func (f *fakeProvider) Voices() []string      { return nil }
func (f *fakeProvider) DefaultFormat() string { return f.format }
func (f *fakeProvider) Synthesize(ctx context.Context, req tts.Request) (tts.Audio, error) {
	return tts.Audio{Data: []byte("AUDIO"), Format: f.format}, nil
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
