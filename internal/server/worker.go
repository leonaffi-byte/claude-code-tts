package server

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ybouhjira/claude-code-tts/internal/logging"
	"github.com/ybouhjira/claude-code-tts/internal/tts"
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
	resolver    synthResolver
	player      audioPlayer
	jobs        chan *Job
	jobHistory  []*Job
	historyMu   sync.RWMutex
	workerCount int
	queueSize   int
	processed   atomic.Int64
	failed      atomic.Int64
	paused      atomic.Bool
	wg          sync.WaitGroup
	shutdown    chan struct{}
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

// Start launches the worker goroutines
func (wp *WorkerPool) Start() {
	for i := 0; i < wp.workerCount; i++ {
		wp.wg.Add(1)
		go wp.worker(i)
	}
	logging.Info("Started %d TTS workers with queue size %d", wp.workerCount, wp.queueSize)
}

// Stop gracefully shuts down the worker pool
func (wp *WorkerPool) Stop() {
	logging.Info("Stopping worker pool...")
	close(wp.shutdown)
	close(wp.jobs)
	wp.wg.Wait()
	logging.Info("Worker pool stopped (processed=%d, failed=%d)", wp.processed.Load(), wp.failed.Load())
}

// worker processes jobs from the queue
func (wp *WorkerPool) worker(id int) {
	defer wp.wg.Done()
	logging.Debug("Worker %d started", id)

	for {
		select {
		case <-wp.shutdown:
			logging.Debug("Worker %d shutting down", id)
			return
		case job, ok := <-wp.jobs:
			if !ok {
				logging.Debug("Worker %d: jobs channel closed", id)
				return
			}
			// Check if paused, wait until resumed
			for wp.paused.Load() {
				select {
				case <-wp.shutdown:
					logging.Debug("Worker %d: shutdown while paused", id)
					return
				case <-time.After(100 * time.Millisecond):
					// Continue checking pause status
				}
			}
			logging.Debug("Worker %d processing job %s", id, job.ID)
			wp.processJob(job)
		}
	}
}

// processJob handles a single TTS job
func (wp *WorkerPool) processJob(job *Job) {
	startTime := time.Now()
	logging.Info("Job %s: starting (profile=%s, voice=%s, text_len=%d)", job.ID, job.Profile, job.Voice, len(job.Text))

	job.mu.Lock()
	job.Status = "processing"
	job.mu.Unlock()

	var provider tts.Provider
	var req tts.Request
	var err error
	if job.Provider != "" {
		provider, req, err = wp.resolver.ResolveVoice(job.Provider, job.Voice, job.Speed)
	} else {
		provider, req, err = wp.resolver.Resolve(job.Profile)
		if err == nil {
			if job.Voice != "" {
				req.Voice = job.Voice
			}
			if job.Speed != 0 {
				req.Speed = job.Speed
			}
		}
	}
	if err != nil {
		wp.failJob(job, fmt.Errorf("resolve: %w", err), startTime)
		return
	}
	req.Text = job.Text

	audioOut, err := provider.Synthesize(context.Background(), req)
	if err != nil {
		wp.failJob(job, fmt.Errorf("synthesis: %w", err), startTime)
		return
	}

	if err := wp.player.Play(audioOut.Data, audioOut.Format); err != nil {
		wp.failJob(job, fmt.Errorf("playback: %w", err), startTime)
		return
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

	return PoolStatus{
		WorkerCount:    wp.workerCount,
		QueueSize:      wp.queueSize,
		QueuePending:   len(wp.jobs),
		TotalProcessed: wp.processed.Load(),
		TotalFailed:    wp.failed.Load(),
		IsPlaying:      wp.player.IsPlaying(),
		IsPaused:       wp.paused.Load(),
		RecentJobs:     recentJobs,
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
		case job := <-wp.jobs:
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
