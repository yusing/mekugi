GO ?= go
NPM ?= npm
CLAUDE_BRIDGE_DIR ?= bin/claude-bridge
INSTALL_BIN = $(shell $(GO) env GOBIN GOPATH | awk 'NR == 1 {dir = $$0} NR == 2 {print dir == "" ? $$0 "/bin" : dir}')

.PHONY: install uninstall preview-assets preview-native-ui test test-ui-snapshots update-ui-snapshots

TEST_PACKAGES ?= ./...
TEST_RUN ?= .
TEST_FLAGS ?=
# Process/PTY fixtures spend most of their time waiting, not using a CPU.
TEST_PARALLEL ?= 32
SNAPSHOT ?= ^TestUISnapshot
SNAPSHOT_PACKAGES ?= ./internal/ui/... ./internal/router ./cmd/mekugi

install: preview-assets
	$(MAKE) build-claude-bridge CLAUDE_BRIDGE_DIR="$(INSTALL_BIN)/claude-bridge"
	$(GO) install ./cmd/mekugi ./cmd/mekugi-exec

uninstall:
	$(GO) clean -i ./cmd/mekugi ./cmd/mekugi-exec
	rm -rf "$(INSTALL_BIN)/claude-bridge"

preview-assets:
	go generate ./internal/router/toolplugin

# Interactive native Main fixtures. No Codex process or model requests.
# Plays a fake session automatically; keys act as in the real UI (Ctrl-C clears,
# interrupts, then exits).
preview-native-ui:
	env -u BASH_ENV MEKUGI_NATIVE_UI_PREVIEW=1 $(GO) test ./internal/router -run '^TestNativeUIPreview$$' -count=1 -timeout 0

# Select the changed owner without rebuilding assets or disabling Go's test cache.
test:
	env -u BASH_ENV -u MEKUGI_UPDATE_UI_SNAPSHOTS $(GO) test $(TEST_PACKAGES) -run '$(value TEST_RUN)' -parallel=$(TEST_PARALLEL) $(TEST_FLAGS)

# Offline rendered-output regression checks. Mismatches leave .txt.new candidates.
test-ui-snapshots:
	env -u BASH_ENV -u MEKUGI_UPDATE_UI_SNAPSHOTS $(GO) test ./internal/uisnapshot $(SNAPSHOT_PACKAGES) -run 'TestSnapshot|$(value SNAPSHOT)' -count=1

# Replaces baselines for the selected tests; review candidates/diffs first.
update-ui-snapshots:
	env -u BASH_ENV MEKUGI_UPDATE_UI_SNAPSHOTS=1 $(GO) test $(SNAPSHOT_PACKAGES) -run '$(value SNAPSHOT)' -count=1

# One relocatable runtime bundle owner for preview, installation, and releases.
# Build in a temporary directory so source dependencies/manifests stay untouched.
.PHONY: build-claude-bridge build-claude test-claude test-claude-package
build-claude-bridge:
	@set -eu; \
	bridge_build=$$(mktemp -d); \
	trap 'rm -rf "$$bridge_build"' EXIT; \
	cp internal/claude/bridge/package.json internal/claude/bridge/package-lock.json internal/claude/bridge/tsconfig.json internal/claude/bridge/*.ts "$$bridge_build/"; \
	$(NPM) ci --prefix "$$bridge_build" --ignore-scripts; \
	$(NPM) run build --prefix "$$bridge_build"; \
	$(NPM) prune --prefix "$$bridge_build" --ignore-scripts --omit=dev --omit=optional; \
	(cd "$$bridge_build" && node --input-type=module -e 'await import("@anthropic-ai/claude-agent-sdk"); await import("zod")'); \
	rm -f "$$bridge_build"/dist/*.test.js; \
	mkdir -p "$(CLAUDE_BRIDGE_DIR)"; \
	rm -rf "$(CLAUDE_BRIDGE_DIR)/dist" "$(CLAUDE_BRIDGE_DIR)/node_modules"; \
	cp -R "$$bridge_build/dist" "$$bridge_build/node_modules" "$$bridge_build/package.json" "$$bridge_build/package-lock.json" "$(CLAUDE_BRIDGE_DIR)/"

# Development preview only. Never replaces the installed mekugi binary.
build-claude: build-claude-bridge
	$(GO) build -o bin/mekugi ./cmd/mekugi
	$(GO) build -o bin/mekugi-exec ./cmd/mekugi-exec

# Offline bridge compilation and tests; native acceptance remains opt-in.
test-claude:
	$(NPM) ci --prefix internal/claude/bridge --ignore-scripts
	rm -rf internal/claude/bridge/dist
	$(NPM) run build --prefix internal/claude/bridge
	node --test internal/claude/bridge/dist/*.test.js
	$(MAKE) test TEST_PACKAGES='./internal/claude ./internal/session ./cmd/mekugi'
	$(MAKE) test TEST_PACKAGES=./internal/router TEST_RUN='NativeRuntime|UISnapshotNativeRuntime'

# Uses a preassembled package; the smoke test extracts and relocates it itself.
test-claude-package:
	test -n "$(CLAUDE_PACKAGE_DIR)"
	MEKUGI_TEST_CLAUDE_PACKAGE="$(abspath $(CLAUDE_PACKAGE_DIR))" $(MAKE) test TEST_PACKAGES=./cmd/mekugi TEST_RUN='^TestClaudePackagedLaunch$$' TEST_FLAGS='-count=1 -v'
