package cost

import (
	"math"
	"testing"
)

func TestCentsFor(t *testing.T) {
	// tts-1 = $15 / 1M chars; 1000 chars -> $0.015 -> 1.5 cents.
	if got := CentsFor("openai", "tts-1", 1000); math.Abs(got-1.5) > 1e-9 {
		t.Errorf("tts-1 1000 chars = %v cents, want 1.5", got)
	}
	// tts-1-hd = $30 / 1M chars; 1000 chars -> 3.0 cents.
	if got := CentsFor("openai", "tts-1-hd", 1000); math.Abs(got-3.0) > 1e-9 {
		t.Errorf("tts-1-hd 1000 chars = %v cents, want 3.0", got)
	}
	// Empty model uses the provider default (tts-1).
	if got := CentsFor("openai", "", 1000); math.Abs(got-1.5) > 1e-9 {
		t.Errorf("openai default 1000 chars = %v cents, want 1.5", got)
	}
	// Unknown provider/model -> 0.
	if got := CentsFor("nope", "x", 1000); got != 0 {
		t.Errorf("unknown = %v, want 0", got)
	}
}

func TestEffectiveModel(t *testing.T) {
	if EffectiveModel("openai", "") != "tts-1" {
		t.Errorf("openai default = %q, want tts-1", EffectiveModel("openai", ""))
	}
	if EffectiveModel("openai", "tts-1-hd") != "tts-1-hd" {
		t.Errorf("explicit model not preserved")
	}
	if EffectiveModel("grok", "") != "grok" {
		t.Errorf("grok default = %q, want grok", EffectiveModel("grok", ""))
	}
}

func TestModelsFor(t *testing.T) {
	if got := ModelsFor("openai"); len(got) != 3 || got[0] != "tts-1" {
		t.Errorf("openai models = %v", got)
	}
	if got := ModelsFor("nope"); got != nil {
		t.Errorf("unknown models = %v, want nil", got)
	}
}
