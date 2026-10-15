GO ?= go
# Reuse compiled packages across implementation, review, and staged checkouts.
GO_BUILD_FLAGS ?= -trimpath
# Bound concurrent compiler work; callers can select larger local budgets.
GOMAXPROCS ?= 2
GOMEMLIMIT ?= 1536MiB
export GOMAXPROCS GOMEMLIMIT

.PHONY: install uninstall mekugi-pprof preview-native-ui test test-host lint test-ui-snapshots update-ui-snapshots

TEST_PACKAGES ?= ./...
TEST_RUN ?= .
TEST_FLAGS ?=
# Process/PTY fixtures spend most of their time waiting, not using a CPU.
TEST_PARALLEL ?= 32
HOST_ACCEPTANCE_TESTS := TestNativeToolFrontendCodexE2E \
	TestMRunNativeCodexYieldAndWriteStdinE2E \
	TestMChangesNestedNativeCodexE2E \
	TestJournalMCPNativeCodexReadE2E \
	TestJournalHostFinishNativeCodexSpawnE2E \
	TestPostCompactNativeCodexE2E \
	TestAppServerExecTrackNativeCodexSandbox
SNAPSHOT ?= ^TestUISnapshot
SNAPSHOT_PACKAGES ?= ./internal/ui/... ./internal/livediff ./internal/router ./cmd/mekugi

install:
	$(GO) install $(GO_BUILD_FLAGS) ./cmd/mekugi ./cmd/mekugi-exec

uninstall:
	$(GO) clean -i ./cmd/mekugi ./cmd/mekugi-exec

# Diagnostic build, with symbols and the same optimized code as production.
mekugi-pprof:
	$(GO) build $(GO_BUILD_FLAGS) -tags pprof -o bin/mekugi-pprof ./cmd/mekugi
	$(GO) build $(GO_BUILD_FLAGS) -o bin/mekugi-exec ./cmd/mekugi-exec

# Interactive native Main fixtures. No Codex process or model requests.
# Plays a fake session automatically; keys act as in the real UI (Ctrl-C clears,
# interrupts, then exits).
preview-native-ui:
	env -u BASH_ENV MEKUGI_NATIVE_UI_PREVIEW=1 $(GO) test $(GO_BUILD_FLAGS) ./internal/router -run '^TestNativeUIPreview$$' -count=1 -timeout 0

# Select the changed owner without rebuilding assets or disabling Go's test cache.
test:
	env -u BASH_ENV -u MEKUGI_EXEC_TRACK -u MEKUGI_UPDATE_UI_SNAPSHOTS $(GO) test $(GO_BUILD_FLAGS) $(TEST_PACKAGES) -run '$(value TEST_RUN)' -parallel=$(TEST_PARALLEL) $(TEST_FLAGS)

# Installed Codex, isolated local mocks, no installation or live model requests.
test-host:
	@codex --version
	@set -eu; \
	pattern="^($$(printf '%s' '$(strip $(HOST_ACCEPTANCE_TESTS))' | tr ' ' '|'))$$"; \
	if ! available="$$(env -u BASH_ENV -u MEKUGI_EXEC_TRACK -u MEKUGI_UPDATE_UI_SNAPSHOTS $(GO) test $(GO_BUILD_FLAGS) -tags=journal_e2e ./internal/router -list "$$pattern")"; then \
		printf '%s\n' "$$available"; exit 1; \
	fi; \
	for name in $(HOST_ACCEPTANCE_TESTS); do \
		printf '%s\n' "$$available" | grep -Fxq "$$name" || { printf 'Missing host acceptance test: %s\n' "$$name" >&2; exit 1; }; \
	done; \
	$(MAKE) --no-print-directory test TEST_PACKAGES=./internal/router TEST_RUN="$$pattern" TEST_FLAGS='-tags=journal_e2e -count=1 -v'

lint:
	env -u BASH_ENV golangci-lint run
	@set -e; findings="$$(env -u BASH_ENV deadcode -test ./...)"; \
	if [ -n "$$findings" ]; then printf '%s\n' "$$findings"; exit 1; fi

# Offline rendered-output regression checks. Mismatches leave .txt.new candidates.
test-ui-snapshots:
	env -u BASH_ENV -u MEKUGI_UPDATE_UI_SNAPSHOTS $(GO) test $(GO_BUILD_FLAGS) ./internal/uisnapshot $(SNAPSHOT_PACKAGES) -run 'TestSnapshot|$(value SNAPSHOT)' -count=1

# Replaces baselines for the selected tests; review candidates/diffs first.
update-ui-snapshots:
	env -u BASH_ENV MEKUGI_UPDATE_UI_SNAPSHOTS=1 $(GO) test $(GO_BUILD_FLAGS) $(SNAPSHOT_PACKAGES) -run '$(value SNAPSHOT)' -count=1
