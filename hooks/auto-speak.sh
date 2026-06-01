#!/bin/bash

# Auto-speak hook for Claude Code
# POSTs first sentence to TTS relay for async synthesis

RELAY_URL="${CLAUDE_TTS_RELAY_URL:-http://127.0.0.1:8765}"

{
    json=$(cat)
    msg=$(echo "$json" | jq -r '.stop_hook_message // .message // .content // ""' 2>/dev/null)

    # Skip if empty or too short
    [ -z "$msg" ] || [ ${#msg} -lt 30 ] && exit 0

    # Get first sentence (up to first period, max 200 chars)
    summary=$(echo "$msg" | sed 's/\..*/./' | head -c 200)

    # Escape for JSON (handles Unicode and special chars safely)
    escaped=$(printf '%s' "$summary" | python3 -c 'import json,sys; print(json.dumps(sys.stdin.read()))')

    # Fire-and-forget POST; --max-time 2 caps total time; discard output
    curl --silent --max-time 2 \
         --request POST \
         --header "Content-Type: application/json" \
         --data "{\"text\": $escaped}" \
         "$RELAY_URL/ingest" \
         >/dev/null 2>&1 || true
} &

exit 0
