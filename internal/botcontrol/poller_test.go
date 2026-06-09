package botcontrol

import (
	"context"
	"testing"

	"github.com/ybouhjira/claude-code-tts/internal/telegram"
	"github.com/ybouhjira/claude-code-tts/internal/voicemode"
)

type fakeBot struct {
	sentMsgs   []string
	voiceDemos []string
	answers    []string
}

func (f *fakeBot) GetUpdates(ctx context.Context, offset, timeout int) ([]telegram.Update, error) {
	return nil, nil
}
func (f *fakeBot) SendMessage(ctx context.Context, text string, kb [][]telegram.InlineButton) error {
	f.sentMsgs = append(f.sentMsgs, text)
	return nil
}
func (f *fakeBot) AnswerCallback(ctx context.Context, id, text string) error {
	f.answers = append(f.answers, text)
	return nil
}
func (f *fakeBot) SendVoiceWithButton(ctx context.Context, audio []byte, caption string, kb [][]telegram.InlineButton) error {
	f.voiceDemos = append(f.voiceDemos, caption)
	return nil
}

type fakeSource struct{}

func (fakeSource) Voices() []string { return []string{"alloy", "onyx"} }
func (fakeSource) Models() []string { return []string{"tts-1", "tts-1-hd"} }
func (fakeSource) Demo(ctx context.Context, voice string) ([]byte, string, error) {
	return []byte("OGG-" + voice), "opus", nil
}

func newTestPoller(t *testing.T, bot *fakeBot) (*Poller, *voicemode.SettingsStore) {
	t.Helper()
	ss := voicemode.NewSettingsStore(t.TempDir() + "/vs.json")
	return NewPoller(bot, ss, fakeSource{}, 42), ss
}

func TestPoller_VoicesCommandSendsDemos(t *testing.T) {
	bot := &fakeBot{}
	p, _ := newTestPoller(t, bot)
	p.handleUpdate(context.Background(), telegram.Update{
		Message: &telegram.Message{Text: "/voices", Chat: telegram.Chat{ID: 42}},
	})
	if len(bot.voiceDemos) != 2 {
		t.Errorf("got %d demos, want 2 (alloy, onyx)", len(bot.voiceDemos))
	}
}

func TestPoller_ModelCommandSendsMenu(t *testing.T) {
	bot := &fakeBot{}
	p, _ := newTestPoller(t, bot)
	p.handleUpdate(context.Background(), telegram.Update{
		Message: &telegram.Message{Text: "/model", Chat: telegram.Chat{ID: 42}},
	})
	if len(bot.sentMsgs) != 1 {
		t.Errorf("got %d messages, want 1 model menu", len(bot.sentMsgs))
	}
}

func TestPoller_CallbackSetsVoice(t *testing.T) {
	bot := &fakeBot{}
	p, ss := newTestPoller(t, bot)
	p.handleUpdate(context.Background(), telegram.Update{
		CallbackQuery: &telegram.CallbackQuery{ID: "cb1", Data: "voice:onyx", Message: &telegram.Message{Chat: telegram.Chat{ID: 42}}},
	})
	if ss.Get().Voice != "onyx" {
		t.Errorf("voice = %q, want onyx", ss.Get().Voice)
	}
	if len(bot.answers) != 1 {
		t.Errorf("expected an answerCallback")
	}
}

func TestPoller_IgnoresWrongChat(t *testing.T) {
	bot := &fakeBot{}
	p, _ := newTestPoller(t, bot)
	p.handleUpdate(context.Background(), telegram.Update{
		Message: &telegram.Message{Text: "/voices", Chat: telegram.Chat{ID: 999}},
	})
	if len(bot.voiceDemos) != 0 || len(bot.sentMsgs) != 0 {
		t.Errorf("update from wrong chat should be ignored")
	}
}
