GO ?= go
# Show tool results and diagnostics, not recipe commands.
.SILENT:
# Compact tool output when rtk is available; RTK= keeps it raw.
RTK := $(shell rtk --version >/dev/null 2>&1 && printf rtk)
# Package scripts need generic filters; `rtk bun run` is a passthrough.
TOOL_OUTPUT = $(if $(RTK),$(RTK) err)
TEST_OUTPUT = $(if $(RTK),$(RTK) test)
# Native rtk Go filters invoke go from PATH; preserve custom GO commands raw.
GO_OUTPUT = $(if $(filter-out go,$(GO)),,$(RTK)) $(GO)
# Go suppresses passing logs before the workspace runner compacts package rows.
GO_TEST_RUN = $(WORKSPACE) $(if $(RTK),test,run) $(GO) test
# Reuse compiled packages across implementation, review, and staged checkouts.
GO_BUILD_FLAGS ?= -trimpath
.PHONY: build build-binaries claude-worker claude-worker-check webui webui-check webui-fmt generate check-generated workspace-check docs-check install uninstall mekugi-pprof preview-native-ui test test-saved-diff test-live-preview test-host lint test-ui-snapshots update-ui-snapshots

WORKSPACE = $(GO) run $(GO_BUILD_FLAGS) ./apps/mekugi/cmd/workspace
.PHONY: integrate
BUILD_OUTPUT ?= bin
BUILD_LDFLAGS ?= -s -w
NATIVE_PROFILE ?= release
NATIVE_OUTPUT ?= bin/native
NATIVE_ARCHIVE ?= bin/mekugi-native.tar.gz
.PHONY: build-native test-native package-native

TEST_PACKAGES ?= @workspace
TEST_SUPPORT_PACKAGES := ./testsupport/uisnapshot/...
ifeq ($(TEST_PACKAGES),@workspace)
TEST_SUPPORT_PACKAGES :=
endif
TEST_RUN ?= .
TEST_FLAGS ?=
export MEKUGI_E2E_MODEL ?= gpt-6-luna
# Process/PTY fixtures spend most of their time waiting, not using a CPU.
TEST_PARALLEL ?= 32
HOST_ACCEPTANCE_TESTS := TestNativeToolFrontendCodexE2E \
	TestMRunNativeCodexYieldAndWriteStdinE2E \
	TestMChangesNestedNativeCodexE2E \
	TestAppServerExecTrackNativeCodexSandbox
SNAPSHOT ?= ^TestUISnapshot
SNAPSHOT_PACKAGES ?= @snapshots

install: workspace-check webui claude-worker
	$(GO) install $(GO_BUILD_FLAGS) ./apps/mekugi/cmd/mekugi ./apps/mekugi/cmd/mekugi-exec

uninstall:
	$(GO) clean -i ./apps/mekugi/cmd/mekugi ./apps/mekugi/cmd/mekugi-exec

workspace-check:
	$(WORKSPACE) check

docs-check:
	$(WORKSPACE) docs-check

# Main supplies checks and explicitly authorizes status/cleanup steps.
integrate:
	$(WORKSPACE) integrate $(INTEGRATE_ARGS)

build: workspace-check
	$(WORKSPACE) run $(GO_OUTPUT) build $(GO_BUILD_FLAGS) @workspace

build-binaries: webui claude-worker
	$(MAKE) --no-print-directory build
	$(GO_OUTPUT) build $(GO_BUILD_FLAGS) -ldflags='$(BUILD_LDFLAGS)' -o '$(BUILD_OUTPUT)/' ./apps/mekugi/cmd/mekugi ./apps/mekugi/cmd/mekugi-exec

build-native:
	python3 runtime/build.py build --profile '$(NATIVE_PROFILE)' --output '$(NATIVE_OUTPUT)'

test-native:
	python3 runtime/build.py test --profile '$(NATIVE_PROFILE)' --output '$(NATIVE_OUTPUT)'

package-native:
	python3 runtime/build.py package --profile '$(NATIVE_PROFILE)' --output '$(NATIVE_OUTPUT)' --archive '$(NATIVE_ARCHIVE)'

# Browser assets embedded by `mekugi web`. Go tests and plain Go builds work
# without Bun or Node.js; an unbundled UI host reports how to build its assets.
WEBUI := apps/mekugi/internal/webui/app

CLAUDE_WORKER := backends/claude

$(CLAUDE_WORKER)/node_modules/.bun-install: $(CLAUDE_WORKER)/bun.lock $(CLAUDE_WORKER)/package.json
	cd $(CLAUDE_WORKER) && $(RTK) bun install --frozen-lockfile --no-progress --no-summary
	touch $@

claude-worker: $(CLAUDE_WORKER)/node_modules/.bun-install
	cd $(CLAUDE_WORKER) && $(TOOL_OUTPUT) bun run --silent build

claude-worker-check: claude-worker
	cd $(CLAUDE_WORKER) && $(TOOL_OUTPUT) bun run --silent --bun typecheck
	cd $(CLAUDE_WORKER) && $(TEST_OUTPUT) bun run --silent test

$(WEBUI)/node_modules/.bun-install: $(WEBUI)/bun.lock $(WEBUI)/package.json
	cd $(WEBUI) && $(RTK) bun install --frozen-lockfile --no-progress --no-summary
	touch $@

webui: $(WEBUI)/node_modules/.bun-install
	cd $(WEBUI) && $(TOOL_OUTPUT) bun run --silent build

webui-check: $(WEBUI)/node_modules/.bun-install
	cd $(WEBUI) && $(TEST_OUTPUT) bun run --silent test
	cd $(WEBUI) && $(TOOL_OUTPUT) bun run --silent check
	cd $(WEBUI) && $(TOOL_OUTPUT) bun run --silent lint
	cd $(WEBUI) && $(TOOL_OUTPUT) bun run --silent format:check

webui-fmt: $(WEBUI)/node_modules/.bun-install
	cd $(WEBUI) && $(TOOL_OUTPUT) bun run --silent format

generate: workspace-check
	$(WORKSPACE) run $(GO) generate @workspace
	$(WORKSPACE) index-write
	$(GO) run ./apps/mekugi/cmd/sessionapi-schema

check-generated:
	$(WORKSPACE) index-check
	$(GO) run ./apps/mekugi/cmd/sessionapi-schema -check

# Diagnostic build, with symbols and the same optimized code as production.
mekugi-pprof:
	$(GO_OUTPUT) build $(GO_BUILD_FLAGS) -tags pprof -o bin/mekugi-pprof ./apps/mekugi/cmd/mekugi
	$(GO_OUTPUT) build $(GO_BUILD_FLAGS) -o bin/mekugi-exec ./apps/mekugi/cmd/mekugi-exec

# Interactive native Main fixtures. No Codex process or model requests.
# Plays a fake session automatically; keys act as in the real UI (Ctrl-C clears,
# interrupts, then exits).
preview-native-ui:
	env -u BASH_ENV MEKUGI_NATIVE_UI_PREVIEW=1 $(GO) test $(GO_BUILD_FLAGS) ./apps/mekugi/internal/launch -run '^TestNativeUIPreview$$' -count=1 -timeout 0

# Select the changed owner without rebuilding assets or disabling Go's test cache.
test: workspace-check
	env -u BASH_ENV -u MEKUGI_EXEC_TRACK -u MEKUGI_UPDATE_UI_SNAPSHOTS $(GO_TEST_RUN) $(GO_BUILD_FLAGS) $(TEST_PACKAGES) $(TEST_SUPPORT_PACKAGES) -run '$(value TEST_RUN)' -parallel=$(TEST_PARALLEL) $(value TEST_FLAGS)

# Focused semantic owners; TEST_RUN and TEST_FLAGS pass through to test.
test-saved-diff:
	$(MAKE) --no-print-directory test TEST_PACKAGES='./modules/changehistory/presentation ./modules/changehistory/terminalview ./libraries/diffrender'

test-live-preview:
	$(MAKE) --no-print-directory test TEST_PACKAGES='./modules/livepreview/...'

# Installed Codex, isolated local mocks, no installation or live model requests.
test-host:
	@codex --version
	@set -eu; \
	pattern="^($$(printf '%s' '$(strip $(HOST_ACCEPTANCE_TESTS))' | tr ' ' '|'))$$"; \
	if ! available="$$(env -u BASH_ENV -u MEKUGI_EXEC_TRACK -u MEKUGI_UPDATE_UI_SNAPSHOTS $(GO) test $(GO_BUILD_FLAGS) -tags=codex_e2e ./apps/mekugi/internal/launch -list "$$pattern")"; then \
		printf '%s\n' "$$available"; exit 1; \
	fi; \
	for name in $(HOST_ACCEPTANCE_TESTS); do \
		printf '%s\n' "$$available" | grep -Fxq "$$name" || { printf 'Missing host acceptance test: %s\n' "$$name" >&2; exit 1; }; \
	done; \
	$(MAKE) --no-print-directory test TEST_PACKAGES=./apps/mekugi/internal/launch TEST_RUN="$$pattern" TEST_FLAGS='-tags=codex_e2e -count=1 -v'

lint: workspace-check
	env -u BASH_ENV $(WORKSPACE) run golangci-lint run --allow-serial-runners @workspace
	@set -e; findings="$$(env -u BASH_ENV $(WORKSPACE) run deadcode -f='{{range .Funcs}}{{printf "%s.%s\n" $$.Path .Name}}{{end}}' @workspace)"; \
	findings="$$(printf '%s\n' "$$findings" | awk 'NR == FNR { allowed[$$0] = 1; next } /^github.com\/yusing\/mekugi\/testsupport\// { next } NF && !($$0 in allowed)' .deadcode-allowlist -)"; \
	if [ -n "$$findings" ]; then printf '%s\n' "$$findings"; exit 1; fi
	@set -e; findings="$$(env -u BASH_ENV $(WORKSPACE) run deadcode -test @workspace)"; \
	if [ -n "$$findings" ]; then printf '%s\n' "$$findings"; exit 1; fi

# Offline rendered-output regression checks. Mismatches leave .txt.new candidates.
test-ui-snapshots:
	env -u BASH_ENV -u MEKUGI_UPDATE_UI_SNAPSHOTS $(GO_TEST_RUN) $(GO_BUILD_FLAGS) ./testsupport/uisnapshot/... $(SNAPSHOT_PACKAGES) -run 'TestSnapshot|$(value SNAPSHOT)' -count=1

# Replaces baselines for the selected tests; review candidates/diffs first.
update-ui-snapshots:
	env -u BASH_ENV MEKUGI_UPDATE_UI_SNAPSHOTS=1 $(GO_TEST_RUN) $(GO_BUILD_FLAGS) $(SNAPSHOT_PACKAGES) -run '$(value SNAPSHOT)' -count=1
