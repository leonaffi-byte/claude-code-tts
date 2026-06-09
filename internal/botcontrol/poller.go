package botcontrol

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ybouhjira/claude-code-tts/internal/logging"
	"github.com/ybouhjira/claude-code-tts/internal/telegram"
	"github.com/ybouhjira/claude-code-tts/internal/voicemode"
)

// botSender is the Telegram surface the poller needs (satisfied by *telegram.Sender).
type botSender interface {
	GetUpdates(ctx context.Context, offset, timeout int) ([]telegram.Update, error)
	SendMessage(ctx context.Context, text string, keyboard [][]telegram.InlineButton) error
	AnswerCallback(ctx context.Context, callbackID, text string) error
	SendVoiceWithButton(ctx context.Context, audio []byte, caption string, keyboard [][]telegram.InlineButton) error
}

// settingsWriter persists the user's selection (satisfied by *voicemode.SettingsStore).
type settingsWriter interface {
	Get() voicemode.Settings
	SetVoice(string) error
	SetModel(string) error
}

// voiceModelSource exposes the current provider's voices/models and synthesizes
// demo clips.
type voiceModelSource interface {
	Voices() []string
	Models() []string
	Demo(ctx context.Context, voice string) (audio []byte, format string, err error)
}

// Poller turns Telegram messages/button-taps into voice/model selection changes.
type Poller struct {
	bot      botSender
	settings settingsWriter
	src      voiceModelSource
	chatID   int64
}

// NewPoller creates a Poller restricted to a single chat id.
func NewPoller(bot botSender, settings settingsWriter, src voiceModelSource, chatID int64) *Poller {
	return &Poller{bot: bot, settings: settings, src: src, chatID: chatID}
}

// Run long-polls Telegram until ctx is cancelled. It never returns an error;
// transient failures are logged and retried.
func (p *Poller) Run(ctx context.Context) {
	offset := 0
	for {
		if ctx.Err() != nil {
			return
		}
		// Poll timeout (20s) MUST stay under the Sender's 30s HTTP client timeout,
		// or the client aborts the request right as an idle long-poll returns.
		updates, err := p.bot.GetUpdates(ctx, offset, 20)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			logging.Error("botcontrol: getUpdates: %v", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
			}
			continue
		}
		for _, u := range updates {
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}
			p.handleUpdate(ctx, u)
		}
	}
}

// handleUpdate processes a single update (commands and button taps). It ignores
// anything not from the configured chat id.
func (p *Poller) handleUpdate(ctx context.Context, u telegram.Update) {
	switch {
	case u.Message != nil:
		if u.Message.Chat.ID != p.chatID {
			return
		}
		p.handleCommand(ctx, strings.TrimSpace(u.Message.Text))
	case u.CallbackQuery != nil:
		if u.CallbackQuery.Message == nil || u.CallbackQuery.Message.Chat.ID != p.chatID {
			return
		}
		p.handleCallback(ctx, u.CallbackQuery)
	}
}

func (p *Poller) handleCommand(ctx context.Context, text string) {
	cmd := strings.Fields(text)
	if len(cmd) == 0 {
		return
	}
	switch cmd[0] {
	case "/voices":
		for _, v := range p.src.Voices() {
			audio, format, err := p.src.Demo(ctx, v)
			if err != nil {
				logging.Error("botcontrol: demo %q: %v", v, err)
				continue
			}
			_ = format
			kb := [][]telegram.InlineButton{{{Text: "✅ Use " + v, CallbackData: "voice:" + v}}}
			if err := p.bot.SendVoiceWithButton(ctx, audio, "🔊 "+v, kb); err != nil {
				logging.Error("botcontrol: send demo %q: %v", v, err)
			}
		}
	case "/model":
		p.bot.SendMessage(ctx, "Pick a model:", modelKeyboard(p.src.Models()))
	case "/menu":
		st := p.settings.Get()
		p.bot.SendMessage(ctx, fmt.Sprintf("Current voice: %s\nCurrent model: %s",
			orDefault(st.Voice, "(profile default)"), orDefault(st.Model, "(profile default)")),
			modelKeyboard(p.src.Models()))
	case "/help", "/start":
		p.bot.SendMessage(ctx, "Commands:\n/voices — hear each voice and tap to use it\n/model — pick a model\n/menu — show current selection", nil)
	default:
		p.bot.SendMessage(ctx, "Unknown command. Try /voices, /model, /menu, or /help.", nil)
	}
}

func (p *Poller) handleCallback(ctx context.Context, cq *telegram.CallbackQuery) {
	parts := strings.SplitN(cq.Data, ":", 2)
	if len(parts) != 2 {
		p.bot.AnswerCallback(ctx, cq.ID, "unrecognized")
		return
	}
	kind, val := parts[0], parts[1]
	var err error
	var msg string
	switch kind {
	case "voice":
		err = p.settings.SetVoice(val)
		msg = "Voice set to " + val
	case "model":
		err = p.settings.SetModel(val)
		msg = "Model set to " + val
	default:
		p.bot.AnswerCallback(ctx, cq.ID, "unrecognized")
		return
	}
	if err != nil {
		logging.Error("botcontrol: save selection: %v", err)
		p.bot.AnswerCallback(ctx, cq.ID, "couldn't save")
		return
	}
	p.bot.AnswerCallback(ctx, cq.ID, msg)
}

func modelKeyboard(models []string) [][]telegram.InlineButton {
	kb := make([][]telegram.InlineButton, 0, len(models))
	for _, m := range models {
		kb = append(kb, []telegram.InlineButton{{Text: m, CallbackData: "model:" + m}})
	}
	return kb
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
