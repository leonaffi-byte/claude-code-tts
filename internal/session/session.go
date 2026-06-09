// Package session derives a short, human-recognizable label for the current
// Claude Code instance, so voice messages from different sessions sharing one
// Telegram chat can be told apart.
package session

import (
	"os"
	"path/filepath"
	"sync"
)

var (
	once   sync.Once
	cached string
)

// Label returns the session label, computed once. Precedence:
//  1. $CLAUDE_TTS_SESSION (an explicit name the user set)
//  2. the working directory's base name (the project folder)
//  3. "claude"
func Label() string {
	once.Do(func() { cached = computeLabel() })
	return cached
}

func computeLabel() string {
	if l := os.Getenv("CLAUDE_TTS_SESSION"); l != "" {
		return l
	}
	if wd, err := os.Getwd(); err == nil {
		if b := filepath.Base(wd); b != "" && b != "." && b != string(filepath.Separator) {
			return b
		}
	}
	return "claude"
}
