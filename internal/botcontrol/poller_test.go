package botcontrol

import (
	"context"
	"strings"
	"testing"

	"github.com/ybouhjira/claude-code-tts/internal/telegram"
	"github.com/ybouhjira/claude-code-tts/internal/voicemode"
)

type fakeBot struct {
	sentMsgs   []string
	menus      []string
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
func (f *fakeBot) SendMenu(ctx context.Context, text string, rows [][]string) error {
	f.menus = append(f.menus, text)
	return nil
}
func (f *fakeBot) AnswerCallback(ctx context.Context, id, text string) error {
	f.answers = append(f.answers, text)
	return nil
}
func (f *fakeBot) SendClipWithButton(ctx context.Context, audio []byte, format, caption string, kb [][]telegram.InlineButton) error {
	f.voiceDemos = append(f.voiceDemos, caption)
	return nil
}

type fakeSource struct{}

func (fakeSource) Provider() string { return "openai" }
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
	// A clear intro message precedes the demo clips.
	if len(bot.sentMsgs) < 1 || !strings.Contains(bot.sentMsgs[0], "tap") {
		t.Errorf("expected an intro message explaining how to pick, got %v", bot.sentMsgs)
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
	} else if !strings.Contains(bot.sentMsgs[0], "Pick a model") {
		t.Errorf("model menu text = %q, want it to mention 'Pick a model'", bot.sentMsgs[0])
	}
}

func TestPoller_ButtonLabelTriggersVoices(t *testing.T) {
	bot := &fakeBot{}
	p, _ := newTestPoller(t, bot)
	// Tapping the persistent "🎙 Voices" button sends its label text.
	p.handleUpdate(context.Background(), telegram.Update{
		Message: &telegram.Message{Text: btnVoices, Chat: telegram.Chat{ID: 42}},
	})
	if len(bot.voiceDemos) != 2 {
		t.Errorf("tapping the Voices button should send demos, got %d", len(bot.voiceDemos))
	}
}

func TestPoller_PricesListsBothProviders(t *testing.T) {
	bot := &fakeBot{}
	p, _ := newTestPoller(t, bot)
	p.handleUpdate(context.Background(), telegram.Update{
		Message: &telegram.Message{Text: btnPrices, Chat: telegram.Chat{ID: 42}},
	})
	if len(bot.sentMsgs) != 1 {
		t.Fatalf("got %d messages, want 1 price list", len(bot.sentMsgs))
	}
	msg := bot.sentMsgs[0]
	for _, want := range []string{"OpenAI", "Grok", "tts-1", "$15.00", "$5.00"} {
		if !strings.Contains(msg, want) {
			t.Errorf("price list missing %q:\n%s", want, msg)
		}
	}
}

func TestPoller_CallbackSwitchesProvider(t *testing.T) {
	bot := &fakeBot{}
	p, ss := newTestPoller(t, bot)
	_ = ss.SetVoice("alloy") // pre-existing OpenAI voice
	p.handleUpdate(context.Background(), telegram.Update{
		CallbackQuery: &telegram.CallbackQuery{ID: "cb", Data: "provider:grok", Message: &telegram.Message{Chat: telegram.Chat{ID: 42}}},
	})
	got := ss.Get()
	if got.Provider != "grok" {
		t.Errorf("provider = %q, want grok", got.Provider)
	}
	if got.Voice != "" {
		t.Errorf("switching provider must clear the old voice, got %q", got.Voice)
	}
	if len(bot.answers) != 1 {
		t.Errorf("expected an answerCallback")
	}
}

func TestPoller_StartShowsButtonMenu(t *testing.T) {
	bot := &fakeBot{}
	p, _ := newTestPoller(t, bot)
	p.handleUpdate(context.Background(), telegram.Update{
		Message: &telegram.Message{Text: "/start", Chat: telegram.Chat{ID: 42}},
	})
	if len(bot.menus) != 1 {
		t.Errorf("/start should install the button keyboard, got %d menus", len(bot.menus))
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
	// A persistent confirmation message naming the new voice is also sent.
	if len(bot.sentMsgs) != 1 || !strings.Contains(bot.sentMsgs[0], "onyx") {
		t.Errorf("expected a confirmation message naming onyx, got %v", bot.sentMsgs)
	}
}

func TestPoller_IgnoresWrongChat(t *testing.T) {
	bot := &fakeBot{}
	p, ss := newTestPoller(t, bot)
	// A command from the wrong chat must do nothing.
	p.handleUpdate(context.Background(), telegram.Update{
		Message: &telegram.Message{Text: "/voices", Chat: telegram.Chat{ID: 999}},
	})
	// A button tap from the wrong chat must not change settings or be answered.
	p.handleUpdate(context.Background(), telegram.Update{
		CallbackQuery: &telegram.CallbackQuery{ID: "x", Data: "voice:onyx", Message: &telegram.Message{Chat: telegram.Chat{ID: 999}}},
	})
	if len(bot.voiceDemos) != 0 || len(bot.sentMsgs) != 0 || len(bot.answers) != 0 {
		t.Errorf("updates from the wrong chat should be ignored")
	}
	if ss.Get().Voice != "" {
		t.Errorf("wrong-chat callback must not change settings, got voice %q", ss.Get().Voice)
	}
}
