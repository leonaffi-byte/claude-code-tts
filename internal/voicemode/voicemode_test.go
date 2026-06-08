package voicemode

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidAndHelpers(t *testing.T) {
	for _, m := range []Mode{Off, Computer, Phone, Both} {
		if !Valid(m) {
			t.Errorf("%q should be valid", m)
		}
	}
	if Valid("loud") {
		t.Error("\"loud\" should be invalid")
	}
	cases := []struct {
		m               Mode
		local, telegram bool
	}{
		{Off, false, false},
		{Computer, true, false},
		{Phone, false, true},
		{Both, true, true},
	}
	for _, c := range cases {
		if c.m.PlaysLocal() != c.local || c.m.SendsTelegram() != c.telegram {
			t.Errorf("%q: local=%v telegram=%v, want %v/%v", c.m, c.m.PlaysLocal(), c.m.SendsTelegram(), c.local, c.telegram)
		}
	}
}

func TestStore_DefaultWhenMissing(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "nope.json"))
	if got := s.Get(); got != Computer {
		t.Errorf("missing file -> %q, want computer", got)
	}
}

func TestStore_SetGetRoundTrip(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "state.json"))
	if err := s.Set(Phone); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got := s.Get(); got != Phone {
		t.Errorf("got %q, want phone", got)
	}
}

func TestStore_SetInvalid(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "state.json"))
	if err := s.Set("sideways"); err == nil {
		t.Error("expected error for invalid mode")
	}
}

func TestStore_GetInvalidContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := writeFileHelper(path, `{"mode":"wat"}`); err != nil {
		t.Fatal(err)
	}
	s := NewStore(path)
	if got := s.Get(); got != Computer {
		t.Errorf("invalid content -> %q, want computer (default)", got)
	}
}

func writeFileHelper(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}
