package ttsconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_MissingFileReturnsDefault(t *testing.T) {
	t.Setenv("CLAUDE_TTS_CONFIG", filepath.Join(t.TempDir(), "nope.json"))
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.DefaultProvider != "openai" {
		t.Errorf("default provider = %q, want openai", cfg.DefaultProvider)
	}
	if _, ok := cfg.Profiles["default"]; !ok {
		t.Errorf("expected a 'default' profile")
	}
}

func TestLoad_ReadsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	_ = os.WriteFile(path, []byte(`{
      "default_provider": "grok",
      "providers": {"grok": {"api_key_env": "XAI_API_KEY"}},
      "profiles": {"default": {"provider": "grok", "voice": "eve", "speed": 1.1}}
    }`), 0o600)
	t.Setenv("CLAUDE_TTS_CONFIG", path)

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.DefaultProvider != "grok" {
		t.Errorf("provider = %q", cfg.DefaultProvider)
	}
	if cfg.Profiles["default"].Voice != "eve" || cfg.Profiles["default"].Speed != 1.1 {
		t.Errorf("profile = %+v", cfg.Profiles["default"])
	}
}

func TestLoad_MalformedFileErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	_ = os.WriteFile(path, []byte("{not json"), 0o600)
	t.Setenv("CLAUDE_TTS_CONFIG", path)
	if _, err := loadConfig(); err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

func TestConfigPath_ExpandsLeadingTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)        // honored by os.UserHomeDir on unix
	t.Setenv("USERPROFILE", home) // honored by os.UserHomeDir on windows
	t.Setenv("CLAUDE_TTS_CONFIG", "~/sub/config.json")

	got := configPath()
	want := filepath.Join(home, "sub", "config.json")
	if got != want {
		t.Errorf("configPath() = %q, want %q", got, want)
	}
}

func TestLoad_TildeConfigIsHonored(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{
      "default_provider": "grok",
      "providers": {"grok": {"api_key_env": "XAI_API_KEY"}},
      "profiles": {"default": {"provider": "grok", "voice": "eve", "speed": 1.1}}
    }`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_TTS_CONFIG", "~/config.json")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.DefaultProvider != "grok" {
		t.Errorf("tilde config not honored: provider = %q, want grok", cfg.DefaultProvider)
	}
}

func TestExpandHome_NonTildePathsUnchanged(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, p := range []string{"", "~", "/abs/path", "relative/path", "~user/x"} {
		if got := expandHome(p); got != p {
			t.Errorf("expandHome(%q) = %q, want unchanged", p, got)
		}
	}
}

func TestLoadConfig_TelegramSection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{
      "default_provider":"openai",
      "providers":{"openai":{"api_key_env":"OPENAI_API_KEY"}},
      "profiles":{"default":{"provider":"openai","voice":"alloy"}},
      "telegram":{"bot_token_env":"TELEGRAM_BOT_TOKEN","chat_id":"99"}
    }`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_TTS_CONFIG", path)
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Telegram == nil || cfg.Telegram.ChatID != "99" || cfg.Telegram.BotTokenEnv != "TELEGRAM_BOT_TOKEN" {
		t.Errorf("telegram parse wrong: %+v", cfg.Telegram)
	}
}
