package ttsconfig

import "testing"

func testConfig() *Config {
	return &Config{
		DefaultProvider: "grok",
		DefaultProfile:  "default",
		Providers: map[string]ProviderConfig{
			"openai": {APIKeyEnv: "OPENAI_API_KEY", Model: "tts-1"},
			"grok":   {APIKeyEnv: "XAI_API_KEY"},
			"piper":  {Binary: "piper", ModelDir: "/models"},
		},
		Profiles: map[string]Profile{
			"default": {Provider: "grok", Voice: "eve", Speed: 1.1},
			"offline": {Provider: "piper", Voice: "en_US-amy-medium"},
			"bogus":   {Provider: "nope", Voice: "x"},
		},
	}
}

func TestRegistry_Resolve(t *testing.T) {
	t.Setenv("XAI_API_KEY", "xai-k")
	reg, err := NewRegistry(testConfig())
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	prov, req, err := reg.Resolve("default")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if prov.Name() != "grok" || req.Voice != "eve" || req.Speed != 1.1 {
		t.Errorf("got %s/%+v", prov.Name(), req)
	}
}

func TestRegistry_ResolveUnknownProfile(t *testing.T) {
	t.Setenv("XAI_API_KEY", "xai-k")
	reg, _ := NewRegistry(testConfig())
	if _, _, err := reg.Resolve("missing"); err == nil {
		t.Fatal("expected error for unknown profile")
	}
}

func TestRegistry_ResolveUnknownProviderInProfile(t *testing.T) {
	t.Setenv("XAI_API_KEY", "xai-k")
	reg, _ := NewRegistry(testConfig())
	if _, _, err := reg.Resolve("bogus"); err == nil {
		t.Fatal("expected error for profile referencing unknown provider")
	}
}

func TestRegistry_ResolveMissingKey(t *testing.T) {
	t.Setenv("XAI_API_KEY", "") // unset
	reg, _ := NewRegistry(testConfig())
	if _, _, err := reg.Resolve("default"); err == nil {
		t.Fatal("expected error when XAI_API_KEY missing")
	}
}

func TestRegistry_ResolveInvalidVoice(t *testing.T) {
	t.Setenv("XAI_API_KEY", "xai-k")
	cfg := testConfig()
	cfg.Profiles["bad"] = Profile{Provider: "grok", Voice: "not-a-voice"}
	reg, _ := NewRegistry(cfg)
	if _, _, err := reg.Resolve("bad"); err == nil {
		t.Fatal("expected error for invalid grok voice")
	}
}

func TestRegistry_DefaultWithEnvOverrides(t *testing.T) {
	t.Setenv("XAI_API_KEY", "xai-k")
	t.Setenv("CLAUDE_TTS_VOICE", "leo")
	t.Setenv("CLAUDE_TTS_SPEED", "1.4")
	reg, _ := NewRegistry(testConfig())
	prov, req, err := reg.Default()
	if err != nil {
		t.Fatalf("Default: %v", err)
	}
	if prov.Name() != "grok" || req.Voice != "leo" || req.Speed != 1.4 {
		t.Errorf("override failed: %s/%+v", prov.Name(), req)
	}
}

func TestRegistry_DefaultInvalidSpeedDroppedOnProfilePath(t *testing.T) {
	t.Setenv("XAI_API_KEY", "xai-k")
	t.Setenv("CLAUDE_TTS_SPEED", "fast") // not a float -> ignored
	reg, _ := NewRegistry(testConfig())
	_, req, err := reg.Default()
	if err != nil {
		t.Fatalf("Default: %v", err)
	}
	// Profile speed (1.1) must be preserved when the override is invalid.
	if req.Speed != 1.1 {
		t.Errorf("invalid speed override changed profile speed: got %v, want 1.1", req.Speed)
	}
}

func TestRegistry_DefaultInvalidSpeedDroppedOnProviderPath(t *testing.T) {
	t.Setenv("XAI_API_KEY", "xai-k")
	t.Setenv("CLAUDE_TTS_PROVIDER", "grok")
	t.Setenv("CLAUDE_TTS_SPEED", "1,2") // comma -> not a float -> ignored
	reg, _ := NewRegistry(testConfig())
	_, req, err := reg.Default()
	if err != nil {
		t.Fatalf("Default: %v", err)
	}
	// Speed stays 0 (provider default) when the override is invalid.
	if req.Speed != 0 {
		t.Errorf("invalid speed override applied on provider path: got %v, want 0", req.Speed)
	}
}

func TestRegistry_DefaultModelOverrideOnProviderPath(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-k")
	t.Setenv("XAI_API_KEY", "xai-k")
	t.Setenv("CLAUDE_TTS_PROVIDER", "openai")
	t.Setenv("CLAUDE_TTS_MODEL", "tts-1-hd")
	reg, _ := NewRegistry(testConfig())
	_, req, err := reg.Default()
	if err != nil {
		t.Fatalf("Default: %v", err)
	}
	if req.Model != "tts-1-hd" {
		t.Errorf("model override not applied: got %q, want tts-1-hd", req.Model)
	}
}

func TestRegistry_ResolveVoice(t *testing.T) {
	t.Setenv("XAI_API_KEY", "xai-k")
	reg, _ := NewRegistry(testConfig())

	prov, req, err := reg.ResolveVoice("grok", "", 1.0) // empty -> first voice (eve)
	if err != nil {
		t.Fatalf("ResolveVoice: %v", err)
	}
	if prov.Name() != "grok" || req.Voice != "eve" {
		t.Errorf("got %s/%q, want grok/eve", prov.Name(), req.Voice)
	}
	if _, _, err := reg.ResolveVoice("grok", "bogus", 1.0); err == nil {
		t.Error("expected invalid-voice error")
	}
	if _, _, err := reg.ResolveVoice("nope", "x", 1.0); err == nil {
		t.Error("expected unknown-provider error")
	}
}

func TestRegistry_DefaultProviderEnvOverride(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-k")
	t.Setenv("XAI_API_KEY", "xai-k")
	t.Setenv("CLAUDE_TTS_PROVIDER", "openai")
	t.Setenv("CLAUDE_TTS_VOICE", "onyx")
	reg, _ := NewRegistry(testConfig())
	prov, req, err := reg.Default()
	if err != nil {
		t.Fatalf("Default: %v", err)
	}
	if prov.Name() != "openai" || req.Voice != "onyx" {
		t.Errorf("got %s/%q, want openai/onyx", prov.Name(), req.Voice)
	}
}

func TestRegistry_TelegramSender(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "tok")

	// Configured -> sender.
	cfg := testConfig()
	cfg.Telegram = &TelegramConfig{BotTokenEnv: "TELEGRAM_BOT_TOKEN", ChatID: "42"}
	if s, reason := mustRegistry(t, cfg).TelegramSender(); s == nil {
		t.Fatalf("expected a sender, got nil (reason=%q)", reason)
	}

	// Missing chat id -> nil + reason.
	cfg2 := testConfig()
	cfg2.Telegram = &TelegramConfig{BotTokenEnv: "TELEGRAM_BOT_TOKEN", ChatID: ""}
	if s2, r2 := mustRegistry(t, cfg2).TelegramSender(); s2 != nil || r2 == "" {
		t.Errorf("expected nil sender + reason for missing chat id, got %v/%q", s2, r2)
	}

	// No telegram section -> nil + reason.
	if s3, r3 := mustRegistry(t, testConfig()).TelegramSender(); s3 != nil || r3 == "" {
		t.Errorf("expected nil sender + reason when unconfigured, got %v/%q", s3, r3)
	}
}

func mustRegistry(t *testing.T, cfg *Config) *Registry {
	t.Helper()
	r, err := NewRegistry(cfg)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	return r
}
