package ttsconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	BotTokenEnv string `json:"bot_token_env"`
	ChatID      string `json:"chat_id"`
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

// configPath returns CLAUDE_TTS_CONFIG or the default plugin location.
func configPath() string {
	if p := os.Getenv("CLAUDE_TTS_CONFIG"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "plugins", "claude-code-tts", "config.json")
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
