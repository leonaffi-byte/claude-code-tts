package botcontrol

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ybouhjira/claude-code-tts/internal/cost"
	"github.com/ybouhjira/claude-code-tts/internal/logging"
	"github.com/ybouhjira/claude-code-tts/internal/telegram"
	"github.com/ybouhjira/claude-code-tts/internal/voicemode"
)

// botSender is the Telegram surface the poller needs (satisfied by *telegram.Sender).
type botSender interface {
	GetUpdates(ctx context.Context, offset, timeout int) ([]telegram.Update, error)
	SendMessage(ctx context.Context, text string, keyboard [][]telegram.InlineButton) error
	SendMenu(ctx context.Context, text string, rows [][]string) error
	AnswerCallback(ctx context.Context, callbackID, text string) error
	SendClipWithButton(ctx context.Context, audio []byte, format, caption string, keyboard [][]telegram.InlineButton) error
}

// Main-menu button labels. Tapping one of these on the persistent reply keyboard
// sends its text, which handleCommand routes the same as the matching command.
const (
	btnVoices   = "🎙 Voices"
	btnModel    = "🎚 Model"
	btnProvider = "🔀 Provider"
	btnPrices   = "💲 Prices"
	btnMenu     = "⚙️ Menu"
	btnHelp     = "❓ Help"
)

func mainMenuRows() [][]string {
	return [][]string{{btnVoices, btnModel}, {btnProvider, btnPrices}, {btnMenu, btnHelp}}
}

// settingsWriter persists the user's selection (satisfied by *voicemode.SettingsStore).
type settingsWriter interface {
	Get() voicemode.Settings
	SetVoice(string) error
	SetModel(string) error
	SetProvider(string) error
}

// voiceModelSource exposes the current provider's voices/models and synthesizes
// demo clips.
type voiceModelSource interface {
	Provider() string // current effective provider name (e.g. "openai", "grok")
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
	// Skip any updates queued while we were offline so old commands (e.g. a
	// /voices sent yesterday, which would re-bill demos) don't replay on start.
	offset := p.drainBacklog(ctx)
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

// drainBacklog returns the offset just past any already-queued updates, without
// acting on them. Telegram redelivers unacknowledged updates, so a fresh process
// (offset 0) would otherwise reprocess commands sent while it was offline.
func (p *Poller) drainBacklog(ctx context.Context) int {
	offset := 0
	ups, err := p.bot.GetUpdates(ctx, 0, 0)
	if err != nil {
		return 0
	}
	for _, u := range ups {
		if u.UpdateID >= offset {
			offset = u.UpdateID + 1
		}
	}
	return offset
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
	t := strings.TrimSpace(text)
	first := t
	if f := strings.Fields(t); len(f) > 0 {
		first = f[0] // for "/voices …" style commands
	}
	switch {
	case t == btnVoices || first == "/voices":
		p.sendVoices(ctx)
	case t == btnModel || first == "/model":
		p.bot.SendMessage(ctx, "🎚 Pick a model — tap one (higher quality costs more):", modelKeyboard(p.src.Models()))
	case t == btnProvider || first == "/provider":
		p.bot.SendMessage(ctx, "🔀 Pick a provider — tap one (switching resets the voice to that provider's default):", providerKeyboard())
	case t == btnMenu || first == "/menu":
		st := p.settings.Get()
		p.bot.SendMessage(ctx, fmt.Sprintf(
			"⚙️ Current settings\n• Provider: %s\n• Voice: %s\n• Model: %s\n\nTap 🎙 Voices to change the voice, 🔀 Provider to switch provider, or a model below.",
			providerTitle(p.src.Provider()), orDefault(st.Voice, "default"), orDefault(st.Model, "default")),
			modelKeyboard(p.src.Models()))
	case t == btnPrices || first == "/prices":
		p.bot.SendMessage(ctx, pricesMessage(), nil)
	case t == btnHelp || first == "/help" || first == "/start":
		p.sendWelcome(ctx)
	default:
		// Anything else: (re)show the button keyboard so the user never has to type.
		p.bot.SendMenu(ctx, "👇 Tap a button to control my voice.", mainMenuRows())
	}
}

// sendWelcome greets the user and installs the persistent button keyboard.
func (p *Poller) sendWelcome(ctx context.Context) {
	p.bot.SendMenu(ctx,
		"👋 Hi! I speak Claude's replies aloud here. Use the buttons below — no typing needed:\n\n"+
			"• 🎙 Voices — hear each voice, then tap ✅ to use one\n"+
			"• 🎚 Model — choose quality vs. cost\n"+
			"• 🔀 Provider — switch between OpenAI and xAI (Grok)\n"+
			"• 💲 Prices — every model's price on both providers\n"+
			"• ⚙️ Menu — show your current provider, voice & model\n\n"+
			"Every voice message is labeled with the project it came from and its estimated cost.",
		mainMenuRows())
}

// sendVoices plays a demo of each voice, each with a tap-to-use button.
func (p *Poller) sendVoices(ctx context.Context) {
	voices := p.src.Voices()
	if len(voices) == 0 {
		p.bot.SendMessage(ctx, "No voices are available for the current provider.", nil)
		return
	}
	p.bot.SendMessage(ctx, "🎙 Listen to each clip, then tap \"✅ Use …\" under the one you want — it becomes my voice for everything I say next.", nil)
	for _, v := range voices {
		audio, format, err := p.src.Demo(ctx, v)
		if err != nil {
			logging.Error("botcontrol: demo %q: %v", v, err)
			continue
		}
		kb := [][]telegram.InlineButton{{{Text: "✅ Use " + v, CallbackData: "voice:" + v}}}
		if err := p.bot.SendClipWithButton(ctx, audio, format, "🔊 Voice: "+v, kb); err != nil {
			logging.Error("botcontrol: send demo %q: %v", v, err)
		}
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
	case "provider":
		err = p.settings.SetProvider(val)
		msg = "Provider set to " + providerTitle(val) + " — tap 🎙 Voices to pick a voice"
	default:
		p.bot.AnswerCallback(ctx, cq.ID, "unrecognized")
		return
	}
	if err != nil {
		logging.Error("botcontrol: save selection: %v", err)
		p.bot.AnswerCallback(ctx, cq.ID, "⚠️ Couldn't save — please try again")
		return
	}
	// Quick toast on the tapped button, plus a persistent confirmation so it's
	// obvious the change took effect.
	p.bot.AnswerCallback(ctx, cq.ID, msg)
	p.bot.SendMessage(ctx, "✅ "+msg+". I'll use it from now on.", nil)
}

// modelLabels gives the buttons friendlier text; the callback data stays the
// bare model name so the handler keeps working.
var modelLabels = map[string]string{
	"tts-1":           "tts-1 — standard",
	"tts-1-hd":        "tts-1-hd — higher quality",
	"gpt-4o-mini-tts": "gpt-4o-mini-tts — newest",
}

func modelKeyboard(models []string) [][]telegram.InlineButton {
	kb := make([][]telegram.InlineButton, 0, len(models))
	for _, m := range models {
		label := m
		if l, ok := modelLabels[m]; ok {
			label = l
		}
		kb = append(kb, []telegram.InlineButton{{Text: label, CallbackData: "model:" + m}})
	}
	return kb
}

// providerKeyboard offers the switchable cloud providers (OpenAI, Grok).
func providerKeyboard() [][]telegram.InlineButton {
	kb := make([][]telegram.InlineButton, 0, len(cost.ProviderOrder))
	for _, p := range cost.ProviderOrder {
		kb = append(kb, []telegram.InlineButton{{Text: providerTitle(p), CallbackData: "provider:" + p}})
	}
	return kb
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// providerTitle is the display name for a provider id.
func providerTitle(p string) string {
	switch p {
	case "openai":
		return "OpenAI"
	case "grok":
		return "Grok (xAI)"
	}
	return p
}

// pricesMessage lists every model on both providers with its price.
func pricesMessage() string {
	var b strings.Builder
	b.WriteString("💲 TTS model prices — USD per 1,000,000 characters (estimates):\n")
	last := ""
	for _, pl := range cost.AllPrices() {
		if pl.Provider != last {
			b.WriteString("\n" + providerTitle(pl.Provider) + "\n")
			last = pl.Provider
		}
		label := pl.Model
		if l, ok := modelLabels[pl.Model]; ok {
			label = l
		}
		b.WriteString(fmt.Sprintf("• %s — $%.2f\n", label, pl.USDPerMillion))
	}
	b.WriteString("\nRule of thumb: a 200-character reply ≈ $0.003 on tts-1 (~0.3¢).")
	return b.String()
}
