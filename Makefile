.PHONY: build install clean test test-hooks lint run relay help

# Variables
BINARY_NAME=tts-server
CLI_BINARY_NAME=speak-text
RELAY_BINARY_NAME=tts-relay
INSTALL_DIR=$(HOME)/.claude/plugins/claude-code-tts
GO=go
GOFLAGS=-ldflags="-s -w"
# Pinned linter version for reproducible lint results (keep in sync with ci.yml)
GOLANGCI_VERSION=v1.61.0

# Default target
all: build

## build: Build the binaries
build:
	@echo "Building $(BINARY_NAME)..."
	@mkdir -p bin
	$(GO) build $(GOFLAGS) -o bin/$(BINARY_NAME) ./cmd/tts-server
	@echo "Built bin/$(BINARY_NAME)"
	@echo "Building $(CLI_BINARY_NAME)..."
	$(GO) build $(GOFLAGS) -o bin/$(CLI_BINARY_NAME) ./cmd/speak-text
	@echo "Built bin/$(CLI_BINARY_NAME)"
	@echo "Building $(RELAY_BINARY_NAME)..."
	$(GO) build $(GOFLAGS) -o bin/$(RELAY_BINARY_NAME) ./cmd/relay
	@echo "Built bin/$(RELAY_BINARY_NAME)"

## relay: Build only the relay binary
relay:
	@mkdir -p bin
	$(GO) build $(GOFLAGS) -o bin/$(RELAY_BINARY_NAME) ./cmd/relay
	@echo "Built bin/$(RELAY_BINARY_NAME)"

## install: Install plugin to Claude Code plugins directory
install: build
	@echo "Installing to $(INSTALL_DIR)..."
	@mkdir -p $(INSTALL_DIR)/bin
	@mkdir -p $(INSTALL_DIR)/.claude
	@mkdir -p $(INSTALL_DIR)/hooks
	@mkdir -p -m 0700 $(HOME)/.claude/run
	@cp bin/$(BINARY_NAME) $(INSTALL_DIR)/bin/
	@cp bin/$(CLI_BINARY_NAME) $(INSTALL_DIR)/bin/
	@cp bin/$(RELAY_BINARY_NAME) $(INSTALL_DIR)/bin/
	@cp hooks/auto-speak.sh $(INSTALL_DIR)/hooks/
	@chmod +x $(INSTALL_DIR)/hooks/auto-speak.sh
	@cp hooks/relay-start.sh $(INSTALL_DIR)/hooks/
	@chmod +x $(INSTALL_DIR)/hooks/relay-start.sh
	@cp hooks/relay-stop.sh $(INSTALL_DIR)/hooks/
	@chmod +x $(INSTALL_DIR)/hooks/relay-stop.sh
	@cp plugin.json $(INSTALL_DIR)/
	@cp .mcp.json $(INSTALL_DIR)/
	@cp .claude/settings.json $(INSTALL_DIR)/.claude/
	@cp README.md $(INSTALL_DIR)/
	@cp LICENSE $(INSTALL_DIR)/
	@echo "Installed successfully!"
	@echo ""
	@echo "Add this to your claude_desktop_config.json or run:"
	@echo "  claude mcp add tts $(INSTALL_DIR)/bin/$(BINARY_NAME)"

## uninstall: Remove plugin from Claude Code plugins directory
uninstall:
	@echo "Uninstalling from $(INSTALL_DIR)..."
	@rm -rf $(INSTALL_DIR)
	@echo "Uninstalled successfully!"

## clean: Remove build artifacts
clean:
	@echo "Cleaning..."
	@rm -rf bin/
	@echo "Cleaned."

## test: Run tests
test:
	@echo "Running tests..."
	$(GO) test -v ./...

## test-hooks: Run bash-based hook lifecycle tests
test-hooks:
	@echo "Running hook lifecycle tests..."
	sh hooks/relay-lifecycle_test.sh

## test-coverage: Run tests with coverage
test-coverage:
	@echo "Running tests with coverage..."
	$(GO) test -cover -coverprofile=coverage.out ./...
	$(GO) tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

## lint: Run linter
lint:
	@echo "Running linter..."
	@which golangci-lint > /dev/null || (echo "Installing golangci-lint $(GOLANGCI_VERSION)..." && go install github.com/golangci/golangci-lint/cmd/golangci-lint@$(GOLANGCI_VERSION))
	golangci-lint run ./...

## fmt: Format code
fmt:
	@echo "Formatting code..."
	$(GO) fmt ./...

## run: Run the server directly (for development)
run: build
	@echo "Running $(BINARY_NAME)..."
	./bin/$(BINARY_NAME)

## deps: Download dependencies
deps:
	@echo "Downloading dependencies..."
	$(GO) mod download
	$(GO) mod tidy

## update-deps: Update dependencies
update-deps:
	@echo "Updating dependencies..."
	$(GO) get -u ./...
	$(GO) mod tidy

## help: Show this help
help:
	@echo "Claude Code TTS Plugin - Makefile targets:"
	@echo ""
	@sed -n 's/^##//p' $(MAKEFILE_LIST) | column -t -s ':' | sed -e 's/^/ /'
