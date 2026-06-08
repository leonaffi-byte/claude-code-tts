package ttsconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_MissingFileReturnsDefault(t *testing.T) {
	t.Setenv("CLAUDE_TTS_CONFIG", filepath.Join(t.TempDir(), "nope.json"))
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
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

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
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
	if _, err := Load(); err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}
