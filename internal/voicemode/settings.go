package voicemode

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Settings is the user's persisted selection (empty fields = profile default).
type Settings struct {
	Voice    string `json:"voice"`
	Model    string `json:"model"`
	Provider string `json:"provider"` // "" = configured default; e.g. "openai", "grok"
}

// SettingsStore persists Settings to a JSON file.
type SettingsStore struct {
	path string
	mu   sync.Mutex
}

// NewSettingsStore creates a store backed by the file at path.
func NewSettingsStore(path string) *SettingsStore { return &SettingsStore{path: path} }

// DefaultSettingsStore uses ~/.claude/plugins/claude-code-tts/voice-settings.json
// (overridable via CLAUDE_TTS_SETTINGS).
func DefaultSettingsStore() *SettingsStore { return NewSettingsStore(settingsPath()) }

func settingsPath() string {
	if p := os.Getenv("CLAUDE_TTS_SETTINGS"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "plugins", "claude-code-tts", "voice-settings.json")
}

// Get returns the persisted settings, or zero values when the file is missing
// or unreadable.
func (s *SettingsStore) Get() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if err != nil {
		return Settings{}
	}
	var st Settings
	if err := json.Unmarshal(data, &st); err != nil {
		return Settings{}
	}
	return st
}

// SetProvider switches the provider and clears the voice + model, since those
// are provider-specific and would otherwise carry over and be invalid.
func (s *SettingsStore) SetProvider(p string) error {
	return s.update(func(st *Settings) {
		st.Provider = p
		st.Voice = ""
		st.Model = ""
	})
}

// SetVoice updates the voice, preserving the model.
func (s *SettingsStore) SetVoice(v string) error {
	return s.update(func(st *Settings) { st.Voice = v })
}

// SetModel updates the model, preserving the voice.
func (s *SettingsStore) SetModel(m string) error {
	return s.update(func(st *Settings) { st.Model = m })
}

func (s *SettingsStore) update(mut func(*Settings)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var st Settings
	if data, err := os.ReadFile(s.path); err == nil {
		_ = json.Unmarshal(data, &st)
	}
	mut(&st)
	if dir := filepath.Dir(s.path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("voicemode: create dir: %w", err)
		}
	}
	data, _ := json.Marshal(st)
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("voicemode: write settings: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("voicemode: rename settings: %w", err)
	}
	return nil
}
