package ttsconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ProviderConfig is per-provider configuration from the config file.
type ProviderConfig struct {
	APIKeyEnv string `json:"api_key_env"`
	Model     string `json:"model"`     // openai
	Language  string `json:"language"`  // grok
	Binary    string `json:"binary"`    // piper
	ModelDir  string `json:"model_dir"` // piper
}

// Profile is a named (provider, voice, speed, model) selection.
type Profile struct {
	Provider string  `json:"provider"`
	Voice    string  `json:"voice"`
	Speed    float64 `json:"speed"`
	Model    string  `json:"model"`
}

// TelegramConfig configures Telegram delivery. The bot token is read from the
// named environment variable; the chat id is stored directly (not secret).
type TelegramConfig struct {
	BotTokenEnv string         `json:"bot_token_env"`
	ChatID      string         `json:"chat_id"`
	Inbound     *InboundConfig `json:"inbound,omitempty"`
}

// InboundConfig configures the Telegram→Claude return path. Translate is a
// pointer so an omitted value can default to true (on) rather than false.
type InboundConfig struct {
	Enabled         bool   `json:"enabled"`
	TranscribeModel string `json:"transcribe_model"`
	Translate       *bool  `json:"translate"`
	SourceLanguage  string `json:"source_language"`
	TargetLanguage  string `json:"target_language"`
	RequireReply    bool   `json:"require_reply"`
}

// ResolvedInbound is InboundConfig with all defaults applied.
type ResolvedInbound struct {
	Enabled         bool
	TranscribeModel string
	Translate       bool
	SourceLanguage  string
	TargetLanguage  string
	RequireReply    bool
}

// ResolvedInbound applies defaults: transcribe model gpt-4o-mini-transcribe,
// translate on, source auto, target English. A nil TelegramConfig or nil
// Inbound yields a disabled result.
func (t *TelegramConfig) ResolvedInbound() ResolvedInbound {
	in := InboundConfig{}
	if t != nil && t.Inbound != nil {
		in = *t.Inbound
	}
	r := ResolvedInbound{
		Enabled:         in.Enabled,
		TranscribeModel: in.TranscribeModel,
		SourceLanguage:  in.SourceLanguage,
		TargetLanguage:  in.TargetLanguage,
		RequireReply:    in.RequireReply,
		Translate:       true,
	}
	if in.Translate != nil {
		r.Translate = *in.Translate
	}
	if r.TranscribeModel == "" {
		r.TranscribeModel = "gpt-4o-mini-transcribe"
	}
	if r.SourceLanguage == "" {
		r.SourceLanguage = "auto"
	}
	if r.TargetLanguage == "" {
		r.TargetLanguage = "English"
	}
	return r
}

// Config is the on-disk configuration.
type Config struct {
	DefaultProvider string                    `json:"default_provider"`
	DefaultProfile  string                    `json:"default_profile"`
	Providers       map[string]ProviderConfig `json:"providers"`
	Profiles        map[string]Profile        `json:"profiles"`
	Telegram        *TelegramConfig           `json:"telegram,omitempty"`
}

// DefaultConfig is the zero-config, back-compatible OpenAI setup.
func DefaultConfig() *Config {
	return &Config{
		DefaultProvider: "openai",
		DefaultProfile:  "default",
		Providers: map[string]ProviderConfig{
			"openai": {APIKeyEnv: "OPENAI_API_KEY", Model: "tts-1"},
		},
		Profiles: map[string]Profile{
			"default": {Provider: "openai", Voice: "alloy", Speed: 1.0},
		},
	}
}

// configPath returns CLAUDE_TTS_CONFIG or the default plugin location. A
// leading "~/" in CLAUDE_TTS_CONFIG is expanded to the user's home directory so
// the env var honors the same shortcut used elsewhere in the codebase.
func configPath() string {
	if p := os.Getenv("CLAUDE_TTS_CONFIG"); p != "" {
		return expandHome(p)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "plugins", "claude-code-tts", "config.json")
}

// LoadConfigOrDefault reads the config file, returning DefaultConfig() when absent.
func LoadConfigOrDefault() (*Config, error) { return loadConfig() }

// expandHome expands a leading "~/" to the user's home directory. Other paths
// (including a bare "~") are returned unchanged. It mirrors the helper used by
// the Piper provider so "~" is honored consistently for all path inputs.
func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

// loadConfig reads the config file, falling back to DefaultConfig when it is absent.
// A present-but-malformed file is an error.
func loadConfig() (*Config, error) {
	path := configPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultConfig(), nil
		}
		return nil, fmt.Errorf("ttsconfig: read %s: %w", path, err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("ttsconfig: parse %s: %w", path, err)
	}
	if cfg.DefaultProfile == "" {
		cfg.DefaultProfile = "default"
	}
	return &cfg, nil
}
