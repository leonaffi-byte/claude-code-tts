// internal/inbound/poller_test.go
package inbound

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/ybouhjira/claude-code-tts/internal/telegram"
	"github.com/ybouhjira/claude-code-tts/internal/ttsconfig"
)

type fakeInjector struct{ got []string }

func (f *fakeInjector) Inject(text string) error { f.got = append(f.got, text); return nil }

type fakeTranscriber struct{ transcript, translation string }

func (f fakeTranscriber) Transcribe(_ context.Context, _ []byte, _, _ string) (string, error) {
	return f.transcript, nil
}
func (f fakeTranscriber) Translate(_ context.Context, _ string, _ string) (string, error) {
	return f.translation, nil
}

type fakeDownloader struct{}

func (fakeDownloader) DownloadVoice(_ context.Context, _ string) ([]byte, error) {
	return []byte("ogg"), nil
}

func cfg() ttsconfig.ResolvedInbound {
	return ttsconfig.ResolvedInbound{Enabled: true, TranscribeModel: "m", Translate: true,
		SourceLanguage: "he", TargetLanguage: "English"}
}

func TestHandleMessage_TextFromAllowedChat_Injects(t *testing.T) {
	inj := &fakeInjector{}
	p := NewPoller(nil, fakeDownloader{}, fakeTranscriber{}, inj, 42, cfg())
	msg := &telegram.Message{Chat: telegram.Chat{ID: 42}, Text: "do the thing"}
	ok, err := p.handleMessage(context.Background(), msg)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if len(inj.got) != 1 || inj.got[0] != "do the thing" {
		t.Errorf("injected %v", inj.got)
	}
}

func TestHandleMessage_WrongChat_Dropped(t *testing.T) {
	inj := &fakeInjector{}
	p := NewPoller(nil, fakeDownloader{}, fakeTranscriber{}, inj, 42, cfg())
	msg := &telegram.Message{Chat: telegram.Chat{ID: 999}, Text: "evil"}
	ok, _ := p.handleMessage(context.Background(), msg)
	if ok || len(inj.got) != 0 {
		t.Errorf("message from wrong chat must be dropped; injected=%v", inj.got)
	}
}

func TestHandleMessage_Voice_TranscribesTranslatesInjects(t *testing.T) {
	inj := &fakeInjector{}
	p := NewPoller(nil, fakeDownloader{}, fakeTranscriber{transcript: "shalom", translation: "hello"}, inj, 42, cfg())
	msg := &telegram.Message{Chat: telegram.Chat{ID: 42}, Voice: &telegram.Voice{FileID: "f1"}}
	ok, err := p.handleMessage(context.Background(), msg)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if len(inj.got) != 1 || inj.got[0] != "hello" {
		t.Errorf("injected %v, want translated text", inj.got)
	}
}

func TestHandleMessage_RequireReply_DropsNonReply(t *testing.T) {
	inj := &fakeInjector{}
	c := cfg()
	c.RequireReply = true
	p := NewPoller(nil, fakeDownloader{}, fakeTranscriber{}, inj, 42, c)
	msg := &telegram.Message{Chat: telegram.Chat{ID: 42}, Text: "hi"} // no ReplyToMessage
	if ok, _ := p.handleMessage(context.Background(), msg); ok {
		t.Error("require_reply should drop a non-reply message")
	}
}

func TestAcquireSingleFlight_SecondCallerFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inbound.lock")
	rel, ok, err := AcquireSingleFlight(path)
	if err != nil || !ok {
		t.Fatalf("first acquire ok=%v err=%v", ok, err)
	}
	defer func() { _ = rel() }()
	if _, ok2, _ := AcquireSingleFlight(path); ok2 {
		t.Error("second acquire should fail while held")
	}
	_ = errors.New("") // keep errors imported if unused above
}
