package ttsconfig

import (
	"fmt"
	"os"
	"strconv"

	"github.com/ybouhjira/claude-code-tts/internal/logging"
	"github.com/ybouhjira/claude-code-tts/internal/telegram"
	"github.com/ybouhjira/claude-code-tts/internal/tts"
)

// Registry holds instantiated providers and resolves profiles to requests.
type Registry struct {
	cfg       *Config
	providers map[string]tts.Provider
	missing   map[string]string // provider name -> missing-key error message
}

// NewRegistry instantiates providers declared in cfg.Providers.
func NewRegistry(cfg *Config) (*Registry, error) {
	r := &Registry{cfg: cfg, providers: map[string]tts.Provider{}, missing: map[string]string{}}
	for name, pc := range cfg.Providers {
		switch name {
		case "openai":
			key := os.Getenv(nameOr(pc.APIKeyEnv, "OPENAI_API_KEY"))
			if key == "" {
				r.missing[name] = fmt.Sprintf("set $%s", nameOr(pc.APIKeyEnv, "OPENAI_API_KEY"))
			}
			r.providers[name] = tts.NewOpenAIProvider(key, pc.Model)
		case "grok":
			key := os.Getenv(nameOr(pc.APIKeyEnv, "XAI_API_KEY"))
			if key == "" {
				r.missing[name] = fmt.Sprintf("set $%s", nameOr(pc.APIKeyEnv, "XAI_API_KEY"))
			}
			r.providers[name] = tts.NewGrokProvider(key, pc.Language)
		case "piper":
			r.providers[name] = tts.NewPiperProvider(pc.Binary, pc.ModelDir)
		default:
			return nil, fmt.Errorf("ttsconfig: unknown provider type %q", name)
		}
	}
	return r, nil
}

// Load reads the config file and builds a registry.
func Load() (*Registry, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	return NewRegistry(cfg)
}

// LoadOrDefault builds a registry, falling back to the OpenAI default on error.
func LoadOrDefault() *Registry {
	reg, err := Load()
	if err != nil {
		reg, _ = NewRegistry(DefaultConfig())
	}
	return reg
}

// Resolve maps a profile name to its provider and a base Request (no Text).
func (r *Registry) Resolve(profile string) (tts.Provider, tts.Request, error) {
	pr, ok := r.cfg.Profiles[profile]
	if !ok {
		return nil, tts.Request{}, fmt.Errorf("ttsconfig: unknown profile %q", profile)
	}
	prov, ok := r.providers[pr.Provider]
	if !ok {
		return nil, tts.Request{}, fmt.Errorf("ttsconfig: profile %q references unknown provider %q", profile, pr.Provider)
	}
	if msg, bad := r.missing[pr.Provider]; bad {
		return nil, tts.Request{}, fmt.Errorf("ttsconfig: provider %q not ready: %s", pr.Provider, msg)
	}
	if voices := prov.Voices(); len(voices) > 0 && pr.Voice != "" && !contains(voices, pr.Voice) {
		return nil, tts.Request{}, fmt.Errorf("ttsconfig: voice %q is not valid for provider %q (valid: %v)", pr.Voice, pr.Provider, voices)
	}
	if pr.Model != "" {
		noteModelOverride(prov, pr.Model)
	}
	return prov, tts.Request{Voice: pr.Voice, Speed: pr.Speed, Model: pr.Model}, nil
}

// ResolveVoice resolves an explicit provider + voice (+ speed), bypassing
// profiles. An empty voice uses the provider's first listed voice; providers
// with no fixed voice list (Piper) require an explicit voice.
func (r *Registry) ResolveVoice(provider, voice string, speed float64) (tts.Provider, tts.Request, error) {
	prov, ok := r.providers[provider]
	if !ok {
		return nil, tts.Request{}, fmt.Errorf("ttsconfig: unknown provider %q", provider)
	}
	if msg, bad := r.missing[provider]; bad {
		return nil, tts.Request{}, fmt.Errorf("ttsconfig: provider %q not ready: %s", provider, msg)
	}
	voices := prov.Voices()
	if voice == "" {
		if len(voices) == 0 {
			return nil, tts.Request{}, fmt.Errorf("ttsconfig: provider %q requires an explicit voice", provider)
		}
		voice = voices[0]
	} else if len(voices) > 0 && !contains(voices, voice) {
		return nil, tts.Request{}, fmt.Errorf("ttsconfig: voice %q is not valid for provider %q (valid: %v)", voice, provider, voices)
	}
	return prov, tts.Request{Voice: voice, Speed: speed}, nil
}

// Default resolves the configured default selection, applying CLAUDE_TTS_* env
// overrides. CLAUDE_TTS_PROVIDER (if set) switches to explicit provider+voice
// resolution; otherwise the default profile is used with field overrides.
func (r *Registry) Default() (tts.Provider, tts.Request, error) {
	if pv := os.Getenv("CLAUDE_TTS_PROVIDER"); pv != "" {
		speed, _ := speedOverride() // 0 (provider default) when unset/invalid
		prov, req, err := r.ResolveVoice(pv, os.Getenv("CLAUDE_TTS_VOICE"), speed)
		if err != nil {
			return nil, tts.Request{}, err
		}
		if m := os.Getenv("CLAUDE_TTS_MODEL"); m != "" {
			req.Model = m
			noteModelOverride(prov, m)
		}
		return prov, req, nil
	}

	profile := r.cfg.DefaultProfile
	if p := os.Getenv("CLAUDE_TTS_PROFILE"); p != "" {
		profile = p
	}
	prov, req, err := r.Resolve(profile)
	if err != nil {
		return nil, tts.Request{}, err
	}
	if v := os.Getenv("CLAUDE_TTS_VOICE"); v != "" {
		req.Voice = v
	}
	if m := os.Getenv("CLAUDE_TTS_MODEL"); m != "" {
		req.Model = m
		noteModelOverride(prov, m)
	}
	if f, ok := speedOverride(); ok {
		req.Speed = f
	}
	return prov, req, nil
}

// speedOverride parses CLAUDE_TTS_SPEED. It returns (value, true) on success.
// When the variable is unset it returns (0, false); when it is set but not a
// valid float it logs a warning and returns (0, false) so the override is
// observably dropped rather than silently ignored.
func speedOverride() (float64, bool) {
	s := os.Getenv("CLAUDE_TTS_SPEED")
	if s == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		logging.Warn("ignoring invalid CLAUDE_TTS_SPEED=%q: %v", s, err)
		return 0, false
	}
	return f, true
}

// noteModelOverride logs a debug note when a model override (from CLAUDE_TTS_MODEL
// or a config profile's "model") is requested for a provider that ignores it.
// Only the OpenAI provider reads Request.Model; Grok and Piper ignore it, so the
// override is a no-op for them.
func noteModelOverride(prov tts.Provider, model string) {
	if prov != nil && prov.Name() != "openai" {
		logging.Debug("model override %q ignored: provider %q does not support a model override (OpenAI only)", model, prov.Name())
	}
}

// TelegramSender builds a Telegram sender from config + env, or returns
// (nil, reason) describing why it is unavailable.
func (r *Registry) TelegramSender() (*telegram.Sender, string) {
	tc := r.cfg.Telegram
	if tc == nil {
		return nil, "no \"telegram\" section in config.json"
	}
	env := nameOr(tc.BotTokenEnv, "TELEGRAM_BOT_TOKEN")
	token := os.Getenv(env)
	if token == "" {
		return nil, fmt.Sprintf("set $%s", env)
	}
	if tc.ChatID == "" {
		return nil, "set telegram.chat_id in config.json"
	}
	return telegram.NewSender(token, tc.ChatID), ""
}

// nameOr returns name when non-empty, otherwise fallback. It returns an
// env-var *name* (e.g. "OPENAI_API_KEY"); it does not read the environment.
func nameOr(name, fallback string) string {
	if name != "" {
		return name
	}
	return fallback
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
