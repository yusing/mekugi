GO ?= go
# Compact noninteractive output when rtk is available; RTK= keeps raw output.
RTK := $(shell rtk --version >/dev/null 2>&1 && printf rtk)
# Native rtk Go filters invoke go from PATH; preserve custom GO commands raw.
GO_OUTPUT = $(if $(filter-out go,$(GO)),,$(RTK)) $(GO)
# Reuse compiled packages across implementation, review, and staged checkouts.
GO_BUILD_FLAGS ?= -trimpath
.PHONY: build build-binaries generate check-generated workspace-check install uninstall mekugi-pprof preview-native-ui test test-host lint test-ui-snapshots update-ui-snapshots

WORKSPACE = $(GO) run $(GO_BUILD_FLAGS) ./apps/mekugi/cmd/workspace
BUILD_OUTPUT ?= bin
BUILD_LDFLAGS ?= -s -w

TEST_PACKAGES ?= @workspace
TEST_SUPPORT_PACKAGES := ./testsupport/uisnapshot/...
ifeq ($(TEST_PACKAGES),@workspace)
TEST_SUPPORT_PACKAGES :=
endif
TEST_RUN ?= .
TEST_FLAGS ?=
# Process/PTY fixtures spend most of their time waiting, not using a CPU.
TEST_PARALLEL ?= 32
HOST_ACCEPTANCE_TESTS := TestNativeToolFrontendCodexE2E \
	TestMRunNativeCodexYieldAndWriteStdinE2E \
	TestMChangesNestedNativeCodexE2E \
	TestAppServerExecTrackNativeCodexSandbox
SNAPSHOT ?= ^TestUISnapshot
SNAPSHOT_PACKAGES ?= @workspace

install: workspace-check
	$(GO) install $(GO_BUILD_FLAGS) ./apps/mekugi/cmd/mekugi ./apps/mekugi/cmd/mekugi-exec

uninstall:
	$(GO) clean -i ./apps/mekugi/cmd/mekugi ./apps/mekugi/cmd/mekugi-exec

workspace-check:
	$(WORKSPACE) check

build: workspace-check
	$(WORKSPACE) run $(GO_OUTPUT) build $(GO_BUILD_FLAGS) @workspace

build-binaries: build
	$(GO_OUTPUT) build $(GO_BUILD_FLAGS) -ldflags='$(BUILD_LDFLAGS)' -o '$(BUILD_OUTPUT)/' ./apps/mekugi/cmd/mekugi ./apps/mekugi/cmd/mekugi-exec

generate: workspace-check
	$(WORKSPACE) run $(GO) generate @workspace
	$(WORKSPACE) index-write

check-generated:
	$(WORKSPACE) index-check

# Diagnostic build, with symbols and the same optimized code as production.
mekugi-pprof:
	$(GO_OUTPUT) build $(GO_BUILD_FLAGS) -tags pprof -o bin/mekugi-pprof ./apps/mekugi/cmd/mekugi
	$(GO_OUTPUT) build $(GO_BUILD_FLAGS) -o bin/mekugi-exec ./apps/mekugi/cmd/mekugi-exec

# Interactive native Main fixtures. No Codex process or model requests.
# Plays a fake session automatically; keys act as in the real UI (Ctrl-C clears,
# interrupts, then exits).
preview-native-ui:
	env -u BASH_ENV MEKUGI_NATIVE_UI_PREVIEW=1 $(GO) test $(GO_BUILD_FLAGS) ./apps/mekugi/internal/router -run '^TestNativeUIPreview$$' -count=1 -timeout 0

# Select the changed owner without rebuilding assets or disabling Go's test cache.
test: workspace-check
	env -u BASH_ENV -u MEKUGI_EXEC_TRACK -u MEKUGI_UPDATE_UI_SNAPSHOTS $(WORKSPACE) run $(GO_OUTPUT) test $(GO_BUILD_FLAGS) $(TEST_PACKAGES) $(TEST_SUPPORT_PACKAGES) -run '$(value TEST_RUN)' -parallel=$(TEST_PARALLEL) $(TEST_FLAGS)

# Installed Codex, isolated local mocks, no installation or live model requests.
test-host:
	@codex --version
	@set -eu; \
	pattern="^($$(printf '%s' '$(strip $(HOST_ACCEPTANCE_TESTS))' | tr ' ' '|'))$$"; \
	if ! available="$$(env -u BASH_ENV -u MEKUGI_EXEC_TRACK -u MEKUGI_UPDATE_UI_SNAPSHOTS $(GO) test $(GO_BUILD_FLAGS) -tags=codex_e2e ./apps/mekugi/internal/router -list "$$pattern")"; then \
		printf '%s\n' "$$available"; exit 1; \
	fi; \
	for name in $(HOST_ACCEPTANCE_TESTS); do \
		printf '%s\n' "$$available" | grep -Fxq "$$name" || { printf 'Missing host acceptance test: %s\n' "$$name" >&2; exit 1; }; \
	done; \
	$(MAKE) --no-print-directory test TEST_PACKAGES=./apps/mekugi/internal/router TEST_RUN="$$pattern" TEST_FLAGS='-tags=codex_e2e -count=1 -v'

lint: workspace-check
	env -u BASH_ENV $(WORKSPACE) run golangci-lint run @workspace
	@set -e; findings="$$(env -u BASH_ENV $(WORKSPACE) run deadcode -f='{{range .Funcs}}{{printf "%s.%s\n" $$.Path .Name}}{{end}}' @workspace)"; \
	findings="$$(printf '%s\n' "$$findings" | awk 'NR == FNR { allowed[$$0] = 1; next } /^github.com\/yusing\/mekugi\/testsupport\// { next } NF && !($$0 in allowed)' .deadcode-allowlist -)"; \
	if [ -n "$$findings" ]; then printf '%s\n' "$$findings"; exit 1; fi
	@set -e; findings="$$(env -u BASH_ENV $(WORKSPACE) run deadcode -test @workspace)"; \
	if [ -n "$$findings" ]; then printf '%s\n' "$$findings"; exit 1; fi

# Offline rendered-output regression checks. Mismatches leave .txt.new candidates.
test-ui-snapshots:
	env -u BASH_ENV -u MEKUGI_UPDATE_UI_SNAPSHOTS $(WORKSPACE) run $(GO_OUTPUT) test $(GO_BUILD_FLAGS) ./testsupport/uisnapshot/... $(SNAPSHOT_PACKAGES) -run 'TestSnapshot|$(value SNAPSHOT)' -count=1

# Replaces baselines for the selected tests; review candidates/diffs first.
update-ui-snapshots:
	env -u BASH_ENV MEKUGI_UPDATE_UI_SNAPSHOTS=1 $(WORKSPACE) run $(GO_OUTPUT) test $(GO_BUILD_FLAGS) $(SNAPSHOT_PACKAGES) -run '$(value SNAPSHOT)' -count=1
