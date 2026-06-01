#!/bin/sh
# relay-lifecycle_test.sh — London-school tests for relay-start.sh and relay-stop.sh
#
# These tests define the BEHAVIOR CONTRACT for the lifecycle scripts.
# They FAIL now (scripts don't exist yet) — failing for the right reason.
#
# Design:
#   - No real relay binary, no real network calls, no real pidfile dirs
#   - Each test gets an isolated temp dir torn down on exit
#   - A mock "tts-relay" (a sleep script) replaces the real binary
#   - PLUGIN_ROOT and PIDFILE are overridden per-test
#   - Pass/Fail counted; non-zero exit on any failure

PASS=0
FAIL=0

# Resolve script directory (POSIX-compatible)
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
RELAY_START="$SCRIPT_DIR/relay-start.sh"
RELAY_STOP="$SCRIPT_DIR/relay-stop.sh"

# ── Helpers ────────────────────────────────────────────────────────────────

# make_tmpdir — create an isolated temp dir; print its path
make_tmpdir() {
    mktemp -d /tmp/relay_test_XXXXXX
}

# make_mock_relay DIR — write a mock tts-relay to DIR/bin/tts-relay that
# just sleeps so we can kill it and inspect the pidfile
make_mock_relay() {
    _dir="$1"
    mkdir -p "$_dir/bin"
    cat > "$_dir/bin/tts-relay" <<'EOF'
#!/bin/sh
# Mock relay: stays alive until killed
sleep 60
EOF
    chmod +x "$_dir/bin/tts-relay"
}

# assert_pass LABEL STATUS — record a pass if STATUS==0, else fail
assert_pass() {
    _label="$1"
    _status="$2"
    if [ "$_status" -eq 0 ]; then
        printf 'PASS  %s\n' "$_label"
        PASS=$((PASS + 1))
    else
        printf 'FAIL  %s  (exit=%d, expected 0)\n' "$_label" "$_status"
        FAIL=$((FAIL + 1))
    fi
}

# assert_fail LABEL STATUS — record a pass if STATUS!=0, else fail
assert_fail() {
    _label="$1"
    _status="$2"
    if [ "$_status" -ne 0 ]; then
        printf 'PASS  %s\n' "$_label"
        PASS=$((PASS + 1))
    else
        printf 'FAIL  %s  (exit=0, expected non-zero)\n' "$_label"
        FAIL=$((FAIL + 1))
    fi
}

# assert_file_exists LABEL PATH — record a pass if PATH exists, else fail
assert_file_exists() {
    _label="$1"
    _path="$2"
    if [ -f "$_path" ]; then
        printf 'PASS  %s\n' "$_label"
        PASS=$((PASS + 1))
    else
        printf 'FAIL  %s  (file not found: %s)\n' "$_label" "$_path"
        FAIL=$((FAIL + 1))
    fi
}

# assert_file_absent LABEL PATH — record a pass if PATH does NOT exist, else fail
assert_file_absent() {
    _label="$1"
    _path="$2"
    if [ ! -f "$_path" ]; then
        printf 'PASS  %s\n' "$_label"
        PASS=$((PASS + 1))
    else
        printf 'FAIL  %s  (file should not exist: %s)\n' "$_label" "$_path"
        FAIL=$((FAIL + 1))
    fi
}

# assert_process_dead LABEL PID — record a pass if PID is no longer alive
assert_process_dead() {
    _label="$1"
    _pid="$2"
    if ! kill -0 "$_pid" 2>/dev/null; then
        printf 'PASS  %s\n' "$_label"
        PASS=$((PASS + 1))
    else
        printf 'FAIL  %s  (process %d still alive)\n' "$_label" "$_pid"
        FAIL=$((FAIL + 1))
        # Clean up so the mock does not linger
        kill "$_pid" 2>/dev/null || true
    fi
}

# ── TEST 1: No-op when TTS disabled (env var unset) ─────────────────────────
# Contract: relay-start.sh exits 0 immediately without starting anything
# when CLAUDE_TTS_ENABLED is not set.
test_noop_when_disabled_unset() {
    _tmp="$(make_tmpdir)"
    _pidfile="$_tmp/tts-relay.pid"
    make_mock_relay "$_tmp"

    PLUGIN_ROOT="$_tmp" PIDFILE="$_pidfile" \
        sh "$RELAY_START"
    _exit=$?

    assert_pass  "noop_disabled_unset: exits 0"          "$_exit"
    assert_file_absent "noop_disabled_unset: no pidfile"  "$_pidfile"

    rm -rf "$_tmp"
}

# ── TEST 2: No-op when TTS disabled (env var set to non-true value) ──────────
# Contract: relay-start.sh exits 0 and does NOT start the relay when
# CLAUDE_TTS_ENABLED is set to something other than the literal string "true".
test_noop_when_disabled_false() {
    _tmp="$(make_tmpdir)"
    _pidfile="$_tmp/tts-relay.pid"
    make_mock_relay "$_tmp"

    CLAUDE_TTS_ENABLED="false" PLUGIN_ROOT="$_tmp" PIDFILE="$_pidfile" \
        sh "$RELAY_START"
    _exit=$?

    assert_pass  "noop_disabled_false: exits 0"          "$_exit"
    assert_file_absent "noop_disabled_false: no pidfile"  "$_pidfile"

    rm -rf "$_tmp"
}

# ── TEST 3: Starts relay when enabled and no pidfile exists ──────────────────
# Contract: relay-start.sh launches the relay binary in background and writes
# a pidfile when CLAUDE_TTS_ENABLED=true and no prior pidfile exists.
test_starts_relay_when_enabled_no_pidfile() {
    _tmp="$(make_tmpdir)"
    _pidfile="$_tmp/tts-relay.pid"
    make_mock_relay "$_tmp"

    CLAUDE_TTS_ENABLED="true" PLUGIN_ROOT="$_tmp" PIDFILE="$_pidfile" \
        sh "$RELAY_START"
    _exit=$?

    assert_pass        "start_no_pidfile: exits 0"       "$_exit"
    assert_file_exists "start_no_pidfile: pidfile written" "$_pidfile"

    # Verify the PID in the file is a live process
    if [ -f "$_pidfile" ]; then
        _pid="$(cat "$_pidfile")"
        if kill -0 "$_pid" 2>/dev/null; then
            printf 'PASS  start_no_pidfile: relay process is alive (pid=%d)\n' "$_pid"
            PASS=$((PASS + 1))
        else
            printf 'FAIL  start_no_pidfile: relay process not alive (pid=%d)\n' "$_pid"
            FAIL=$((FAIL + 1))
        fi
        # Tear down the background mock
        kill "$_pid" 2>/dev/null || true
    fi

    rm -rf "$_tmp"
}

# ── TEST 4: Idempotent — skips when relay already running ────────────────────
# Contract: relay-start.sh exits 0 WITHOUT starting a second relay when a
# pidfile already exists containing a live PID.
# We use $$ (the current shell's PID) as the "already running" PID.
test_idempotent_when_already_running() {
    _tmp="$(make_tmpdir)"
    _pidfile="$_tmp/tts-relay.pid"
    make_mock_relay "$_tmp"

    # Pre-populate pidfile with current shell PID (always alive during the test)
    printf '%d\n' "$$" > "$_pidfile"

    CLAUDE_TTS_ENABLED="true" PLUGIN_ROOT="$_tmp" PIDFILE="$_pidfile" \
        sh "$RELAY_START"
    _exit=$?

    assert_pass "idempotent_running: exits 0" "$_exit"

    # Pidfile should still contain the original PID (not overwritten)
    _stored_pid="$(cat "$_pidfile" 2>/dev/null)"
    if [ "$_stored_pid" = "$$" ]; then
        printf 'PASS  idempotent_running: pidfile unchanged (pid=$$)\n'
        PASS=$((PASS + 1))
    else
        printf 'FAIL  idempotent_running: pidfile was overwritten (got %s, want %d)\n' \
            "$_stored_pid" "$$"
        FAIL=$((FAIL + 1))
    fi

    rm -rf "$_tmp"
}

# ── TEST 5: Stale pidfile — cleans up and restarts ───────────────────────────
# Contract: when a pidfile exists but the stored PID is dead, relay-start.sh
# removes the stale pidfile and starts a fresh relay.
# PID 99999999 is vanishingly unlikely to be alive on any real system.
test_stale_pidfile_cleaned_and_relay_started() {
    _tmp="$(make_tmpdir)"
    _pidfile="$_tmp/tts-relay.pid"
    make_mock_relay "$_tmp"

    # Stale pidfile — PID that does not exist
    printf '99999999\n' > "$_pidfile"

    CLAUDE_TTS_ENABLED="true" PLUGIN_ROOT="$_tmp" PIDFILE="$_pidfile" \
        sh "$RELAY_START"
    _exit=$?

    assert_pass        "stale_pidfile: exits 0"           "$_exit"
    assert_file_exists "stale_pidfile: new pidfile written" "$_pidfile"

    # The new PID must not be 99999999
    _new_pid="$(cat "$_pidfile" 2>/dev/null)"
    if [ "$_new_pid" != "99999999" ] && [ -n "$_new_pid" ]; then
        printf 'PASS  stale_pidfile: pidfile updated to fresh pid (%s)\n' "$_new_pid"
        PASS=$((PASS + 1))
    else
        printf 'FAIL  stale_pidfile: pidfile not updated (got %s)\n' "$_new_pid"
        FAIL=$((FAIL + 1))
    fi

    # Verify fresh relay is alive, then tear it down
    if [ -n "$_new_pid" ] && kill -0 "$_new_pid" 2>/dev/null; then
        printf 'PASS  stale_pidfile: fresh relay process alive (pid=%s)\n' "$_new_pid"
        PASS=$((PASS + 1))
        kill "$_new_pid" 2>/dev/null || true
    else
        printf 'FAIL  stale_pidfile: fresh relay process not alive (pid=%s)\n' "$_new_pid"
        FAIL=$((FAIL + 1))
    fi

    rm -rf "$_tmp"
}

# ── TEST 6: Stop-by-pidfile kills relay and removes pidfile ──────────────────
# Contract: relay-stop.sh reads the pidfile, sends SIGTERM to the stored PID,
# and removes the pidfile.
test_stop_kills_relay_and_removes_pidfile() {
    _tmp="$(make_tmpdir)"
    _pidfile="$_tmp/tts-relay.pid"
    make_mock_relay "$_tmp"

    # Start a real background process to kill
    "$_tmp/bin/tts-relay" &
    _mock_pid=$!

    # Write its PID to the pidfile (simulating what relay-start.sh would do)
    printf '%d\n' "$_mock_pid" > "$_pidfile"

    # Brief pause to ensure the background process is schedulable
    # (POSIX sh has no sleep fractions, but 1 s is acceptable in a test)
    sleep 1

    PIDFILE="$_pidfile" \
        sh "$RELAY_STOP"
    _exit=$?

    assert_pass   "stop_relay: exits 0"              "$_exit"
    assert_file_absent "stop_relay: pidfile removed"  "$_pidfile"
    assert_process_dead "stop_relay: process terminated" "$_mock_pid"

    rm -rf "$_tmp"
}

# ── TEST 7: Missing relay binary — exits non-zero with stderr message ────────
# Contract: relay-start.sh must exit non-zero and write to stderr when
# CLAUDE_TTS_ENABLED=true but the relay binary does not exist.
# We deliberately do NOT call make_mock_relay so the bin/ directory is absent.
test_missing_relay_binary_exits_nonzero() {
    _tmp="$(make_tmpdir)"
    _pidfile="$_tmp/tts-relay.pid"
    # No make_mock_relay — $PLUGIN_ROOT/bin/tts-relay does not exist

    _stderr="$(CLAUDE_TTS_ENABLED="true" PLUGIN_ROOT="$_tmp" PIDFILE="$_pidfile" \
        sh "$RELAY_START" 2>&1 1>/dev/null)"
    _exit=$?

    assert_fail        "missing_binary: exits non-zero"           "$_exit"
    assert_file_absent "missing_binary: no pidfile created"       "$_pidfile"

    # stderr must contain a meaningful message identifying the missing binary
    if printf '%s' "$_stderr" | grep -q "relay"; then
        printf 'PASS  missing_binary: stderr contains relay context\n'
        PASS=$((PASS + 1))
    else
        printf 'FAIL  missing_binary: stderr was empty or unhelpful (%s)\n' "$_stderr"
        FAIL=$((FAIL + 1))
    fi

    rm -rf "$_tmp"
}

# ── TEST 8: relay-stop.sh with no pidfile — exits 0 cleanly ──────────────────
# Contract: relay-stop.sh treats the absence of a pidfile as "relay not running"
# and exits 0 without error (idempotent stop).
test_stop_no_pidfile_exits_zero() {
    _tmp="$(make_tmpdir)"
    _pidfile="$_tmp/tts-relay.pid"
    # Explicitly ensure pidfile does not exist
    rm -f "$_pidfile"

    PIDFILE="$_pidfile" \
        sh "$RELAY_STOP"
    _exit=$?

    assert_pass "stop_no_pidfile: exits 0" "$_exit"

    rm -rf "$_tmp"
}

# ── TEST 9: relay-stop.sh with stale pidfile — exits 0, removes pidfile ──────
# Contract: relay-stop.sh detects that the stored PID is dead, removes the
# stale pidfile, and exits 0. PID 99999999 is used as a reliably dead PID.
test_stop_stale_pidfile_exits_zero_and_removes_file() {
    _tmp="$(make_tmpdir)"
    _pidfile="$_tmp/tts-relay.pid"

    # Write a stale pidfile — this PID will never be alive
    printf '99999999\n' > "$_pidfile"

    PIDFILE="$_pidfile" \
        sh "$RELAY_STOP"
    _exit=$?

    assert_pass        "stop_stale_pidfile: exits 0"         "$_exit"
    assert_file_absent "stop_stale_pidfile: pidfile removed" "$_pidfile"

    rm -rf "$_tmp"
}

# ── TEST 10: CLAUDE_TTS_ENABLED=1 (numeric) — treated as disabled ────────────
# Contract: only the literal string "true" enables the relay.
# Setting CLAUDE_TTS_ENABLED=1 must not start the relay — exits 0, no pidfile.
test_noop_when_enabled_numeric_one() {
    _tmp="$(make_tmpdir)"
    _pidfile="$_tmp/tts-relay.pid"
    make_mock_relay "$_tmp"

    CLAUDE_TTS_ENABLED="1" PLUGIN_ROOT="$_tmp" PIDFILE="$_pidfile" \
        sh "$RELAY_START"
    _exit=$?

    assert_pass        "noop_enabled_numeric_1: exits 0"     "$_exit"
    assert_file_absent "noop_enabled_numeric_1: no pidfile"  "$_pidfile"

    rm -rf "$_tmp"
}

# ── Run all tests ─────────────────────────────────────────────────────────────

printf '\n=== relay-lifecycle tests ===\n\n'

test_noop_when_disabled_unset
test_noop_when_disabled_false
test_starts_relay_when_enabled_no_pidfile
test_idempotent_when_already_running
test_stale_pidfile_cleaned_and_relay_started
test_stop_kills_relay_and_removes_pidfile
test_missing_relay_binary_exits_nonzero
test_stop_no_pidfile_exits_zero
test_stop_stale_pidfile_exits_zero_and_removes_file
test_noop_when_enabled_numeric_one

printf '\n=== Results: %d passed, %d failed ===\n\n' "$PASS" "$FAIL"

[ "$FAIL" -eq 0 ]
