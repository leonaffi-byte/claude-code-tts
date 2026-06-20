#!/bin/bash

# Auto-speak hook for Claude Code
# POSTs first sentence to TTS relay for async synthesis

RELAY_URL="${CLAUDE_TTS_RELAY_URL:-http://127.0.0.1:8765}"

{
    # Gate: only run when explicitly enabled
    [ "${CLAUDE_TTS_ENABLED}" = "true" ] || exit 0

    # jq is required to parse the hook payload and build the JSON body.
    # Exit quietly (with a one-line diagnostic) when it is missing so the
    # session is never blocked by a missing optional dependency.
    if ! command -v jq >/dev/null 2>&1; then
        echo "auto-speak: jq not found on PATH; skipping TTS (install jq to enable)" >&2
        exit 0
    fi

    json=$(cat)
    msg=$(echo "$json" | jq -r '.stop_hook_message // .message // .content // ""' 2>/dev/null)

    # Skip if empty or too short
    if [ -z "$msg" ] || [ "${#msg}" -lt 30 ]; then
        exit 0
    fi

    # Get first sentence (up to first period, max 200 chars)
    summary=$(echo "$msg" | sed 's/\..*/./' | head -c 200)

    # Build the JSON body with jq (handles Unicode and special chars safely;
    # no python3 dependency).
    body=$(jq -n --arg t "$summary" '{text: $t}')

    # Fire-and-forget POST; --max-time 2 caps total time; discard output
    curl --silent --max-time 2 \
         --request POST \
         --header "Content-Type: application/json" \
         --data "$body" \
         "$RELAY_URL/ingest" \
         >/dev/null 2>&1 || true
} &

exit 0
