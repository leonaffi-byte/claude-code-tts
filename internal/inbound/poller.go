// internal/inbound/poller.go
// Package inbound polls Telegram for replies and injects them into the live
// claude session. Only messages from the configured chat id are accepted.
package inbound

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/ybouhjira/claude-code-tts/internal/logging"
	"github.com/ybouhjira/claude-code-tts/internal/telegram"
	"github.com/ybouhjira/claude-code-tts/internal/ttsconfig"
)

// Injector writes a line of text into the live claude session.
type Injector interface {
	Inject(text string) error
}

// Updater long-polls Telegram for updates.
type Updater interface {
	GetUpdates(ctx context.Context, offset, timeoutSec int) ([]telegram.Update, error)
}

// VoiceDownloader fetches a voice note's bytes by file id.
type VoiceDownloader interface {
	DownloadVoice(ctx context.Context, fileID string) ([]byte, error)
}

// Transcriber turns audio into (optionally translated) text.
type Transcriber interface {
	Transcribe(ctx context.Context, ogg []byte, model, language string) (string, error)
	Translate(ctx context.Context, text, target string) (string, error)
}

// Poller wires the receiver, transcriber and injector together.
type Poller struct {
	updater     Updater
	downloader  VoiceDownloader
	transcriber Transcriber
	injector    Injector
	chatID      int64
	cfg         ttsconfig.ResolvedInbound
	offset      int
}

// NewPoller constructs a Poller. updater may be nil in unit tests that only
// exercise handleMessage.
func NewPoller(u Updater, d VoiceDownloader, t Transcriber, inj Injector, chatID int64, cfg ttsconfig.ResolvedInbound) *Poller {
	return &Poller{updater: u, downloader: d, transcriber: t, injector: inj, chatID: chatID, cfg: cfg}
}

// Run long-polls until ctx is cancelled. Transient errors are logged and retried
// with a short backoff; the loop never exits on a recoverable error.
func (p *Poller) Run(ctx context.Context) error {
	logging.Info("telegram inbound poller started (chat_id=%d)", p.chatID)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		ups, err := p.updater.GetUpdates(ctx, p.offset, 50)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			logging.Error("inbound getUpdates: %v", err)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
			}
			continue
		}
		for _, up := range ups {
			p.offset = up.UpdateID + 1
			if up.Message == nil {
				continue
			}
			if _, err := p.handleMessage(ctx, up.Message); err != nil {
				logging.Error("inbound handle message: %v", err)
			}
		}
	}
}

// handleMessage applies the security filter, derives text (transcribing voice
// notes), and injects it. Returns whether anything was injected.
func (p *Poller) handleMessage(ctx context.Context, msg *telegram.Message) (bool, error) {
	if msg.Chat.ID != p.chatID {
		logging.Error("inbound: dropping message from unauthorized chat %d", msg.Chat.ID)
		return false, nil
	}
	if p.cfg.RequireReply && msg.ReplyToMessage == nil {
		logging.Debug("inbound: dropping non-reply message (require_reply on)")
		return false, nil
	}

	text := strings.TrimSpace(msg.Text)
	if msg.Voice != nil {
		ogg, err := p.downloader.DownloadVoice(ctx, msg.Voice.FileID)
		if err != nil {
			return false, err
		}
		transcript, err := p.transcriber.Transcribe(ctx, ogg, p.cfg.TranscribeModel, p.cfg.SourceLanguage)
		if err != nil {
			return false, err
		}
		text = strings.TrimSpace(transcript)
		if p.cfg.Translate && text != "" {
			translated, err := p.transcriber.Translate(ctx, text, p.cfg.TargetLanguage)
			if err != nil {
				return false, err
			}
			text = strings.TrimSpace(translated)
		}
	}
	if text == "" {
		return false, nil
	}
	if err := p.injector.Inject(text); err != nil {
		return false, err
	}
	logging.Info("inbound: injected %d chars", len(text))
	return true, nil
}

// AcquireSingleFlight creates an exclusive lock file at path. ok is false (with
// nil error) when another process already holds it, so only one wrapped session
// consumes Telegram updates at a time.
func AcquireSingleFlight(path string) (release func() error, ok bool, err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return func() error {
		_ = f.Close()
		return os.Remove(path)
	}, true, nil
}
