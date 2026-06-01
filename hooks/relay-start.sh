#!/bin/sh
# relay-start.sh — SessionStart hook: starts tts-relay when CLAUDE_TTS_ENABLED=true
#
# Idempotent: if a pidfile exists and the stored process is alive, exits 0
# immediately without starting a duplicate relay.
#
# Note: if Claude Code is killed hard (SIGKILL), the SessionStop hook cannot
# run; the relay will remain alive as an orphan. The next session will detect
# the running relay via this idempotency check and not start a duplicate.

# Fast exit if TTS is disabled — zero overhead when not in use
[ "${CLAUDE_TTS_ENABLED}" = "true" ] || exit 0

PIDFILE="${PIDFILE:-${HOME}/.claude/run/tts-relay.pid}"
PLUGIN_ROOT="${PLUGIN_ROOT:-${CLAUDE_PLUGIN_ROOT:-${HOME}/.claude/plugins/claude-code-tts}}"
RELAY_BIN="${PLUGIN_ROOT}/bin/tts-relay"

# Health check: if pidfile exists and process is alive, relay is already running
if [ -f "$PIDFILE" ]; then
    pid="$(cat "$PIDFILE")"
    if kill -0 "$pid" 2>/dev/null; then
        exit 0  # already running — idempotent
    fi
    # Stale pidfile — remove it and fall through to start
    rm -f "$PIDFILE"
fi

# Verify relay binary is executable
if [ ! -x "$RELAY_BIN" ]; then
    printf 'relay-start: relay binary not found or not executable: %s\n' "$RELAY_BIN" >&2
    exit 1
fi

# Create run directory for pidfile and log (co-located with pidfile)
PIDFILE_DIR="$(dirname "$PIDFILE")"
mkdir -p "$PIDFILE_DIR"
LOG="${PIDFILE_DIR}/tts-relay.log"

# Start relay in background; fire-and-forget
"$RELAY_BIN" >> "$LOG" 2>&1 &
printf '%d\n' "$!" > "$PIDFILE"

exit 0
