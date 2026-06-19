package ttsconfig

import "testing"

func TestResolvedInbound_Defaults(t *testing.T) {
	// Inbound block present but mostly empty: defaults fill in.
	tc := &TelegramConfig{Inbound: &InboundConfig{Enabled: true}}
	got := tc.ResolvedInbound()
	if !got.Enabled {
		t.Fatal("Enabled should be true")
	}
	if got.TranscribeModel != "gpt-4o-mini-transcribe" {
		t.Errorf("TranscribeModel default = %q, want gpt-4o-mini-transcribe", got.TranscribeModel)
	}
	if !got.Translate {
		t.Error("Translate should default to true when omitted")
	}
	if got.SourceLanguage != "auto" || got.TargetLanguage != "English" {
		t.Errorf("language defaults = %q/%q, want auto/English", got.SourceLanguage, got.TargetLanguage)
	}
}

func TestResolvedInbound_TranslateExplicitFalse(t *testing.T) {
	no := false
	tc := &TelegramConfig{Inbound: &InboundConfig{Enabled: true, Translate: &no}}
	if tc.ResolvedInbound().Translate {
		t.Error("Translate should honor explicit false")
	}
}

func TestResolvedInbound_NilTelegram(t *testing.T) {
	var tc *TelegramConfig
	if tc.ResolvedInbound().Enabled {
		t.Error("nil telegram config should yield disabled inbound")
	}
}
