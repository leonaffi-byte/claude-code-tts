#!/bin/sh
# relay-stop.sh — stop the tts-relay using its pidfile
#
# Exits 0 in all cases (including "already stopped") so it is safe to call
# idempotently. Sends SIGTERM to give the relay a chance to flush/close
# connections before exiting.

PIDFILE="${PIDFILE:-${HOME}/.claude/run/tts-relay.pid}"

# No pidfile — relay was never started or already cleaned up
if [ ! -f "$PIDFILE" ]; then
    printf 'relay-stop: relay not running (no pidfile)\n' >&2
    exit 0
fi

pid="$(cat "$PIDFILE")"

# Validate PID is a positive integer to avoid passing garbage to kill
case "$pid" in
    ''|*[!0-9]*)
        printf 'relay-stop: pidfile contains invalid PID: %s\n' "$pid" >&2
        rm -f "$PIDFILE"
        exit 0
        ;;
esac

# Process already dead — clean up stale pidfile
if ! kill -0 "$pid" 2>/dev/null; then
    printf 'relay-stop: relay not running (stale pidfile, pid=%s)\n' "$pid" >&2
    rm -f "$PIDFILE"
    exit 0
fi

# Send SIGTERM and remove pidfile
kill -TERM "$pid"
rm -f "$PIDFILE"

exit 0
