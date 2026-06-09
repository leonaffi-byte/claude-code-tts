package voicemode

import (
	"path/filepath"
	"testing"
)

func TestSettingsStore_DefaultEmpty(t *testing.T) {
	s := NewSettingsStore(filepath.Join(t.TempDir(), "vs.json"))
	got := s.Get()
	if got.Voice != "" || got.Model != "" {
		t.Errorf("missing file -> %+v, want empty", got)
	}
}

func TestSettingsStore_SetGet(t *testing.T) {
	s := NewSettingsStore(filepath.Join(t.TempDir(), "vs.json"))
	if err := s.SetVoice("onyx"); err != nil {
		t.Fatalf("SetVoice: %v", err)
	}
	if err := s.SetModel("tts-1-hd"); err != nil {
		t.Fatalf("SetModel: %v", err)
	}
	got := s.Get()
	if got.Voice != "onyx" || got.Model != "tts-1-hd" {
		t.Errorf("got %+v, want onyx/tts-1-hd", got)
	}
	// A fresh store over the same file reads the persisted values.
	if got2 := NewSettingsStore(s.path).Get(); got2.Voice != "onyx" || got2.Model != "tts-1-hd" {
		t.Errorf("reload got %+v", got2)
	}
}
