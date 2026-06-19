package server

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ybouhjira/claude-code-tts/internal/logging"
	"github.com/ybouhjira/claude-code-tts/internal/tts"
	"github.com/ybouhjira/claude-code-tts/internal/voicemode"
)

// audioPlayer is the playback dependency (satisfied by *audio.Player).
type audioPlayer interface {
	Play(data []byte, format string) error
	IsPlaying() bool
}

// synthResolver resolves profiles/providers to providers + base requests.
type synthResolver interface {
	Resolve(profile string) (tts.Provider, tts.Request, error)
	ResolveVoice(provider, voice string, speed float64) (tts.Provider, tts.Request, error)
	Default() (tts.Provider, tts.Request, error)
}

// modeReader reads the current voice output mode (satisfied by *voicemode.Store).
type modeReader interface {
	Get() voicemode.Mode
}

// telegramSender delivers audio to Telegram (satisfied by *telegram.Sender).
// format selects the message type: "opus" → voice message, else audio file.
type telegramSender interface {
	Send(ctx context.Context, audio []byte, format, caption string) error
}

// SpeakRequest is the input to Submit. When Provider is set it takes precedence
// over Profile; Voice/Speed override the resolved request.
type SpeakRequest struct {
	Text     string
	Profile  string
	Provider string
	Voice    string
	Speed    float64
}

// Job represents a TTS job in the queue
type Job struct {
	ID        string    `json:"id"`
	Text      string    `json:"text"`
	Profile   string    `json:"profile"`
	Provider  string    `json:"provider"`
	Voice     string    `json:"voice"`
	Speed     float64   `json:"speed"`
	CreatedAt time.Time `json:"created_at"`
	Status    string    `json:"status"` // pending, processing, completed, failed
	Error     string    `json:"error,omitempty"`
	mu        sync.RWMutex
}

// WorkerPool manages TTS job processing
type WorkerPool struct {
	resolver       synthResolver
	player         audioPlayer
	mode           modeReader     // nil -> always Computer
	telegram       telegramSender // nil -> Telegram unavailable
	telegramReason string         // why telegram is unavailable (for error messages)
	jobs           chan *Job
	jobHistory     []*Job
	historyMu      sync.RWMutex
	workerCount    int
	queueSize      int
	processed      atomic.Int64
	failed         atomic.Int64
	paused         atomic.Bool
	wg             sync.WaitGroup
	shutdown       chan struct{}

	// submitMu serializes Submit's send against Stop's close(jobs). Submit holds
	// it for reading while sending on jobs; Stop holds it for writing while
	// flipping closed and closing the channel, so a send can never race a close
	// (which would panic with "send on closed channel").
	submitMu sync.RWMutex
	closed   bool      // set true under submitMu once the pool is shutting down
	stopOnce sync.Once // makes Stop idempotent (close-of-closed-channel safe)
}

// NewWorkerPool creates a pool backed by the given resolver and player.
func NewWorkerPool(resolver synthResolver, player audioPlayer, workerCount, queueSize int) *WorkerPool {
	return &WorkerPool{
		resolver:    resolver,
		player:      player,
		jobs:        make(chan *Job, queueSize),
		jobHistory:  make([]*Job, 0),
		workerCount: workerCount,
		queueSize:   queueSize,
		shutdown:    make(chan struct{}),
	}
}

// WithMode sets the voice-mode reader. When unset, the pool behaves as Computer.
func (wp *WorkerPool) WithMode(m modeReader) *WorkerPool { wp.mode = m; return wp }

// WithTelegram sets the Telegram sender (may be nil). reason explains why it is
// unavailable when the sender is nil, for user-facing error messages.
//
// A typed-nil pointer wrapped in the telegramSender interface (e.g.
// (*telegram.Sender)(nil)) is normalized to a true nil interface so the
// downstream `wp.telegram == nil` misconfiguration check fires. This guards
// against a caller wiring a typed-nil sender and accidentally defeating the
// "fail fast when Telegram is unavailable" guarantee.
func (wp *WorkerPool) WithTelegram(s telegramSender, reason string) *WorkerPool {
	if isNilSender(s) {
		s = nil
	}
	wp.telegram = s
	wp.telegramReason = reason
	return wp
}

// isNilSender reports whether s is a true nil interface or a typed-nil pointer
// wrapped in the interface.
func isNilSender(s telegramSender) bool {
	if s == nil {
		return true
	}
	v := reflect.ValueOf(s)
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return v.IsNil()
	}
	return false
}

// Start launches the worker goroutines
func (wp *WorkerPool) Start() {
	for i := 0; i < wp.workerCount; i++ {
		wp.wg.Add(1)
		go wp.worker(i)
	}
	logging.Info("Started %d TTS workers with queue size %d", wp.workerCount, wp.queueSize)
}

// Stop gracefully shuts down the worker pool. Already-queued jobs are drained
// and processed before the workers exit (unless the pool is paused, in which
// case parked workers exit and leave their jobs in the queue). Stop is
// idempotent: repeated calls are safe no-ops.
func (wp *WorkerPool) Stop() {
	wp.stopOnce.Do(func() {
		logging.Info("Stopping worker pool...")
		// Take the write lock so no Submit can be mid-send when we close jobs.
		// Closing jobs lets workers drain buffered items and then observe ok=false;
		// closing shutdown wakes any worker parked in the pause loop.
		wp.submitMu.Lock()
		wp.closed = true
		close(wp.shutdown)
		close(wp.jobs)
		wp.submitMu.Unlock()
	})
	wp.wg.Wait()
	logging.Info("Worker pool stopped (processed=%d, failed=%d)", wp.processed.Load(), wp.failed.Load())
}

// worker processes jobs from the queue
func (wp *WorkerPool) worker(id int) {
	defer wp.wg.Done()
	logging.Debug("Worker %d started", id)

	for {
		// Gate on pause BEFORE dequeuing so a paused worker leaves jobs in the
		// queue where Clear() and the QueuePending count can still see them,
		// rather than parking with a job already pulled out of the channel.
		for wp.paused.Load() {
			select {
			case <-wp.shutdown:
				logging.Debug("Worker %d: shutdown while paused", id)
				return
			case <-time.After(100 * time.Millisecond):
				// Continue checking pause status.
			}
		}

		// Blocking receive: drain all buffered jobs until Stop() closes the
		// channel (ok=false). This is what makes shutdown actually graceful —
		// already-accepted jobs are processed rather than dropped. Stop() also
		// closes wp.shutdown to wake a worker parked in the pause loop above.
		job, ok := <-wp.jobs
		if !ok {
			logging.Debug("Worker %d: jobs channel closed", id)
			return
		}
		logging.Debug("Worker %d processing job %s", id, job.ID)
		wp.processJob(job)
	}
}

// processJob handles a single TTS job
func (wp *WorkerPool) processJob(job *Job) {
	startTime := time.Now()

	mode := voicemode.Computer
	if wp.mode != nil {
		mode = wp.mode.Get()
	}
	logging.Info("Job %s: starting (mode=%s, profile=%s, provider=%s, text_len=%d)", job.ID, mode, job.Profile, job.Provider, len(job.Text))

	job.mu.Lock()
	job.Status = "processing"
	job.mu.Unlock()

	// Off: no synthesis (no cost), no delivery.
	if mode == voicemode.Off {
		job.mu.Lock()
		job.Status = "completed"
		job.mu.Unlock()
		wp.processed.Add(1)
		logging.Info("Job %s: muted (voice mode off)", job.ID)
		return
	}

	var provider tts.Provider
	var req tts.Request
	var err error
	switch {
	case job.Provider != "":
		provider, req, err = wp.resolver.ResolveVoice(job.Provider, job.Voice, job.Speed)
	case job.Profile != "":
		provider, req, err = wp.resolver.Resolve(job.Profile)
	default:
		provider, req, err = wp.resolver.Default()
	}
	if err != nil {
		wp.failJob(job, fmt.Errorf("resolve: %w", err), startTime)
		return
	}
	if job.Voice != "" {
		req.Voice = job.Voice
	}
	if job.Speed != 0 {
		req.Speed = job.Speed
	}
	req.Text = job.Text

	// Fail fast on Telegram misconfiguration before paying for synthesis.
	if mode.SendsTelegram() {
		if wp.telegram == nil {
			wp.failJob(job, fmt.Errorf("telegram not configured: %s", wp.telegramReason), startTime)
			return
		}
		if provider.DefaultFormat() != "mp3" {
			wp.failJob(job, fmt.Errorf("telegram requires an MP3 provider (OpenAI or Grok); %q emits %s", provider.Name(), provider.DefaultFormat()), startTime)
			return
		}
	}

	// NOTE (supersedes the 2026-06-08 spec/plan): the original design listed the
	// Opus/sendVoice voice bubble as a non-goal and described a single synthesis
	// feeding both delivery paths. The shipped behavior intentionally diverges:
	// Telegram receives an Opus voice message and local playback uses the
	// provider's default format (MP3/WAV). Because those formats differ, "both"
	// mode synthesizes TWICE — once below for Telegram and once for local
	// playback — which roughly doubles TTS cost/latency for "both" mode. This is
	// deliberate; the older spec/plan/README cost notes are stale on this point.

	// Telegram delivery: request Opus so it arrives as a voice message (the
	// provider falls back to its default format if it can't emit Opus, in which
	// case Send delivers an audio file instead).
	if mode.SendsTelegram() {
		tgReq := req
		tgReq.Format = "opus"
		tgAudio, err := provider.Synthesize(context.Background(), tgReq)
		if err != nil {
			wp.failJob(job, fmt.Errorf("synthesis (telegram): %w", err), startTime)
			return
		}
		if sendErr := wp.telegram.Send(context.Background(), tgAudio.Data, tgAudio.Format, job.Text); sendErr != nil {
			if !mode.PlaysLocal() {
				wp.failJob(job, fmt.Errorf("telegram: %w", sendErr), startTime)
				return
			}
			logging.Error("Job %s: telegram send failed (continuing to local): %v", job.ID, sendErr)
		}
	}

	// Local playback uses the default format (MP3/WAV).
	if mode.PlaysLocal() {
		localAudio, err := provider.Synthesize(context.Background(), req)
		if err != nil {
			wp.failJob(job, fmt.Errorf("synthesis: %w", err), startTime)
			return
		}
		if err := wp.player.Play(localAudio.Data, localAudio.Format); err != nil {
			wp.failJob(job, fmt.Errorf("playback: %w", err), startTime)
			return
		}
	}

	job.mu.Lock()
	job.Status = "completed"
	job.mu.Unlock()
	wp.processed.Add(1)
	logging.Info("Job %s: completed in %v", job.ID, time.Since(startTime))
}

func (wp *WorkerPool) failJob(job *Job, err error, start time.Time) {
	job.mu.Lock()
	job.Status = "failed"
	job.Error = err.Error()
	job.mu.Unlock()
	wp.failed.Add(1)
	logging.Error("Job %s: %v (after %v)", job.ID, err, time.Since(start))
}

// Submit adds a new job to the queue.
func (wp *WorkerPool) Submit(sr SpeakRequest) (*Job, error) {
	job := &Job{
		ID:        fmt.Sprintf("job-%d", time.Now().UnixNano()),
		Text:      sr.Text,
		Profile:   sr.Profile,
		Provider:  sr.Provider,
		Voice:     sr.Voice,
		Speed:     sr.Speed,
		CreatedAt: time.Now(),
		Status:    "pending",
	}

	wp.historyMu.Lock()
	wp.jobHistory = append(wp.jobHistory, job)
	if len(wp.jobHistory) > 100 {
		wp.jobHistory = wp.jobHistory[1:]
	}
	wp.historyMu.Unlock()

	// Hold submitMu for reading across the send so Stop() cannot close wp.jobs
	// concurrently (a send on a closed channel is "ready" in a select and panics
	// even when a default case is present). The closed flag turns a send-after-
	// shutdown into a clean error instead of a crash.
	wp.submitMu.RLock()
	defer wp.submitMu.RUnlock()

	if wp.closed {
		job.mu.Lock()
		job.Status = "failed"
		job.Error = "pool shutting down"
		job.mu.Unlock()
		return job, fmt.Errorf("worker pool is shutting down")
	}

	select {
	case wp.jobs <- job:
		return job, nil
	default:
		// job is already in jobHistory and visible to GetStatus, so mutate its
		// state under the job lock to avoid racing with a concurrent reader.
		job.mu.Lock()
		job.Status = "failed"
		job.Error = "queue is full"
		job.mu.Unlock()
		return job, fmt.Errorf("job queue is full (size: %d)", wp.queueSize)
	}
}

// Status returns current worker pool statistics
type PoolStatus struct {
	WorkerCount    int    `json:"worker_count"`
	QueueSize      int    `json:"queue_size"`
	QueuePending   int    `json:"queue_pending"`
	TotalProcessed int64  `json:"total_processed"`
	TotalFailed    int64  `json:"total_failed"`
	IsPlaying      bool   `json:"is_playing"`
	IsPaused       bool   `json:"is_paused"`
	RecentJobs     []*Job `json:"recent_jobs,omitempty"`

	VoiceMode          string `json:"voice_mode"`
	TelegramConfigured bool   `json:"telegram_configured"`
}

// GetStatus returns the current pool status
func (wp *WorkerPool) GetStatus() PoolStatus {
	wp.historyMu.RLock()
	recentJobs := make([]*Job, 0)
	start := len(wp.jobHistory) - 10
	if start < 0 {
		start = 0
	}
	// Create deep copies to avoid race conditions with workers modifying jobs
	for _, job := range wp.jobHistory[start:] {
		job.mu.RLock()
		jobCopy := &Job{
			ID:        job.ID,
			Text:      job.Text,
			Profile:   job.Profile,
			Provider:  job.Provider,
			Voice:     job.Voice,
			Speed:     job.Speed,
			CreatedAt: job.CreatedAt,
			Status:    job.Status,
			Error:     job.Error,
		}
		job.mu.RUnlock()
		recentJobs = append(recentJobs, jobCopy)
	}
	wp.historyMu.RUnlock()

	mode := voicemode.Computer
	if wp.mode != nil {
		mode = wp.mode.Get()
	}

	return PoolStatus{
		WorkerCount:    wp.workerCount,
		QueueSize:      wp.queueSize,
		QueuePending:   len(wp.jobs),
		TotalProcessed: wp.processed.Load(),
		TotalFailed:    wp.failed.Load(),
		IsPlaying:      wp.player.IsPlaying(),
		IsPaused:       wp.paused.Load(),
		RecentJobs:     recentJobs,

		VoiceMode:          string(mode),
		TelegramConfigured: wp.telegram != nil,
	}
}

// Pause pauses job processing (queued jobs will wait)
func (wp *WorkerPool) Pause() {
	wp.paused.Store(true)
	logging.Info("Worker pool paused")
}

// Resume resumes job processing
func (wp *WorkerPool) Resume() {
	wp.paused.Store(false)
	logging.Info("Worker pool resumed")
}

// Clear removes all pending jobs from the queue
func (wp *WorkerPool) Clear() int {
	cleared := 0
	for {
		select {
		case job, ok := <-wp.jobs:
			// After Stop() closes wp.jobs, a receive yields (nil, false)
			// immediately; bail out rather than spinning on a nil job.
			if !ok {
				logging.Info("Cleared %d pending jobs from queue", cleared)
				return cleared
			}
			job.mu.Lock()
			job.Status = "cancelled"
			job.Error = "queue cleared"
			job.mu.Unlock()
			cleared++
		default:
			logging.Info("Cleared %d pending jobs from queue", cleared)
			return cleared
		}
	}
}
