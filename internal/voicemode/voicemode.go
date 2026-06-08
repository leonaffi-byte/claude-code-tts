package voicemode

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Mode is the voice output destination.
type Mode string

const (
	Off      Mode = "off"
	Computer Mode = "computer"
	Phone    Mode = "phone"
	Both     Mode = "both"
)

// Valid reports whether m is one of the four known modes.
func Valid(m Mode) bool {
	switch m {
	case Off, Computer, Phone, Both:
		return true
	}
	return false
}

// PlaysLocal reports whether this mode plays on the local speakers.
func (m Mode) PlaysLocal() bool { return m == Computer || m == Both }

// SendsTelegram reports whether this mode sends to Telegram.
func (m Mode) SendsTelegram() bool { return m == Phone || m == Both }

// Store persists the current Mode to a JSON file.
type Store struct {
	path string
	mu   sync.Mutex
}

// NewStore creates a Store backed by the file at path.
func NewStore(path string) *Store { return &Store{path: path} }

// DefaultStore uses ~/.claude/plugins/claude-code-tts/state.json
// (overridable via CLAUDE_TTS_STATE).
func DefaultStore() *Store { return NewStore(statePath()) }

func statePath() string {
	if p := os.Getenv("CLAUDE_TTS_STATE"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "plugins", "claude-code-tts", "state.json")
}

type state struct {
	Mode Mode `json:"mode"`
}

// Get returns the persisted mode, defaulting to Computer when the file is
// missing, unreadable, or holds an invalid value.
func (s *Store) Get() Mode {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if err != nil {
		return Computer
	}
	var st state
	if err := json.Unmarshal(data, &st); err != nil || !Valid(st.Mode) {
		return Computer
	}
	return st.Mode
}

// Set validates m and atomically writes it to the state file.
func (s *Store) Set(m Mode) error {
	if !Valid(m) {
		return fmt.Errorf("voicemode: invalid mode %q (valid: off, computer, phone, both)", m)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if dir := filepath.Dir(s.path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("voicemode: create dir: %w", err)
		}
	}
	data, _ := json.Marshal(state{Mode: m})
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("voicemode: write: %w", err)
	}
	return os.Rename(tmp, s.path)
}
