# Phase 3, Step 5: Makefile Update Report

## Status: green

## Summary

Updated the Makefile to build and install the `tts-relay` binary alongside the existing `tts-server` and `speak-text` binaries.

## Changes Made

**File:** `Makefile`

1. Added `RELAY_BINARY_NAME=tts-relay` to the variables section (line 6), after `CLI_BINARY_NAME=speak-text`.

2. Extended the `build` target with relay build steps (after speak-text block):
   ```makefile
   @echo "Building $(RELAY_BINARY_NAME)..."
   $(GO) build $(GOFLAGS) -o bin/$(RELAY_BINARY_NAME) ./cmd/relay
   @echo "Built bin/$(RELAY_BINARY_NAME)"
   ```

3. Added a standalone `relay` convenience target (after build target, before install):
   ```makefile
   ## relay: Build only the relay binary
   relay:
       @mkdir -p bin
       $(GO) build $(GOFLAGS) -o bin/$(RELAY_BINARY_NAME) ./cmd/relay
       @echo "Built bin/$(RELAY_BINARY_NAME)"
   ```

4. Extended the `install` target to copy relay binary:
   ```makefile
   @cp bin/$(RELAY_BINARY_NAME) $(INSTALL_DIR)/bin/
   ```
   Placed after `@cp bin/$(CLI_BINARY_NAME) $(INSTALL_DIR)/bin/`.

5. Added `relay` to `.PHONY` list.

## Verification

`make build` output:
```
Building tts-server...
go build -ldflags="-s -w" -o bin/tts-server ./cmd/tts-server
Built bin/tts-server
Building speak-text...
go build -ldflags="-s -w" -o bin/speak-text ./cmd/speak-text
Built bin/speak-text
Building tts-relay...
go build -ldflags="-s -w" -o bin/tts-relay ./cmd/relay
Built bin/tts-relay
```

`ls bin/` output:
```
speak-text  tts-relay  tts-server
```

## Key Decisions

- Placed the `relay` standalone target immediately after the `build` target and before `install`, following the logical grouping of build-related targets in the file.
- Used the same `## relay: description` comment pattern as all other targets so the `help` target auto-generates documentation for it.
- The `relay` standalone target includes `@mkdir -p bin` to be safe when invoked independently (the main `build` target already does this before reaching the relay step).

## Concerns / Risks

- None. The changes are purely additive — no existing targets were modified, only extended.
