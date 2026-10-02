GO ?= go

.PHONY: install uninstall preview-assets preview-native-ui test test-ui-snapshots update-ui-snapshots

TEST_PACKAGES ?= ./...
TEST_RUN ?= .
TEST_FLAGS ?=
# Process/PTY fixtures spend most of their time waiting, not using a CPU.
TEST_PARALLEL ?= 32
SNAPSHOT ?= ^TestUISnapshot
SNAPSHOT_PACKAGES ?= ./internal/ui/... ./internal/router ./cmd/mekugi

install: preview-assets
	$(GO) install ./cmd/mekugi ./cmd/mekugi-exec

uninstall:
	$(GO) clean -i ./cmd/mekugi ./cmd/mekugi-exec

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

# Development preview only. Never replaces the installed mekugi binary.
.PHONY: build-claude test-claude
build-claude:
	mkdir -p bin/claude-bridge
	rm -f bin/claude-bridge/*.ts
	rm -rf bin/claude-bridge/dist
	cp internal/claude/bridge/package.json internal/claude/bridge/package-lock.json internal/claude/bridge/tsconfig.json internal/claude/bridge/bridge.ts bin/claude-bridge/
	npm ci --prefix bin/claude-bridge --ignore-scripts
	npm run build --prefix bin/claude-bridge
	$(GO) build -o bin/mekugi ./cmd/mekugi

test-claude:
	npm ci --prefix internal/claude/bridge --ignore-scripts
	rm -rf internal/claude/bridge/dist
	npm run build --prefix internal/claude/bridge
	$(MAKE) test TEST_PACKAGES='./internal/claude ./internal/session ./cmd/mekugi'
	$(MAKE) test TEST_PACKAGES=./internal/router TEST_RUN='NativeRuntime|UISnapshotNativeRuntime'
