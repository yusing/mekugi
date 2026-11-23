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
.PHONY: build build-binaries webui webui-check webui-fmt generate check-generated workspace-check docs-check install uninstall mekugi-pprof preview-native-ui test test-saved-diff test-live-preview test-host lint test-ui-snapshots update-ui-snapshots

WORKSPACE = $(GO) run $(GO_BUILD_FLAGS) ./apps/mekugi/cmd/workspace
.PHONY: integrate
BUILD_OUTPUT ?= bin
BUILD_LDFLAGS ?= -s -w
# Empty uses the native runner's default for each action.
NATIVE_PROFILE ?=
NATIVE_ARGS ?=
NATIVE_TEST_FILTER ?=
NATIVE_OUTPUT ?= bin/native
NATIVE_ARCHIVE ?= bin/mekugi-native.tar.gz
NATIVE_BINARIES = mekugi-app-server codex-code-mode-host $(if $(filter Linux,$(shell uname -s)),bwrap)
GO_INSTALL_DIR = $(or $(shell $(GO) env GOBIN),$(firstword $(subst :, ,$(shell $(GO) env GOPATH)))/bin)
.PHONY: check-native build-native test-native package-native

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
# Go's test cache keys on environment values tests read. Drop values a Mekugi
# launch sets per session, including its frontend PATH entries, and the
# terminal's COLORFGBG, so sessions and terminals reuse cached results, tests
# render one theme, and tests never write to the live session. TMPDIR is
# canonical because macOS temporary paths start with /var -> /private/var, and
# state directories reject symlinked ancestors.
TEST_SESSION_ENV := BASH_ENV COLORFGBG CODEX_THREAD_ID CODEX_ROLLOUT_TRACE_ROOT \
	CODEX_SHELL_GUARD_SOCKET CODEX_SHELL_GUARD_VCS_DIR CODEX_SHELL_GUARD_SUDO_DIR \
	CODEX_SHELL_GUARD_SUDO_COMMAND \
	MEKUGI_AX_OUTPUT MEKUGI_BASE_URL MEKUGI_CONTROL_SOCKET MEKUGI_EXEC_TRACK \
	MEKUGI_SH_TRACKER MEKUGI_UPDATE_UI_SNAPSHOTS MEKUGI_USER_BASH_ENV \
	MEKUGI_VCS_GUARD_HOOK
TEST_ENV = env $(addprefix -u ,$(TEST_SESSION_ENV)) TMPDIR="$$(cd "$${TMPDIR:-/tmp}" && pwd -P)" PATH="$$(printf '%s:' "$$PATH" | \
	awk -v RS=: -v ORS=: -v startup="$${BASH_ENV-}" 'BEGIN { \
		if (startup ~ /\/frontend-bash-env$$/) { sub(/\/frontend-bash-env$$/, "", startup); frontend = startup } \
	} !(frontend && ($$0 == frontend || index($$0, frontend "/") == 1)) && \
	  ($$0 == "" || ($$0 != ENVIRON["CODEX_SHELL_GUARD_VCS_DIR"] && $$0 != ENVIRON["CODEX_SHELL_GUARD_SUDO_DIR"]))' | sed 's/:$$//')"

install: workspace-check webui build-native
	$(GO) install $(GO_BUILD_FLAGS) ./apps/mekugi/cmd/mekugi ./apps/mekugi/cmd/mekugi-exec
	install -m 755 $(foreach file,$(NATIVE_BINARIES),'$(NATIVE_OUTPUT)/$(file)') '$(GO_INSTALL_DIR)/'
	install -m 644 '$(NATIVE_OUTPUT)/mekugi-models.json' '$(GO_INSTALL_DIR)/mekugi-models.json'

uninstall:
	$(GO) clean -i ./apps/mekugi/cmd/mekugi ./apps/mekugi/cmd/mekugi-exec
	rm -f $(foreach file,$(NATIVE_BINARIES) mekugi-models.json,'$(GO_INSTALL_DIR)/$(file)')

workspace-check:
	$(WORKSPACE) check

docs-check:
	$(WORKSPACE) docs-check

# Main supplies checks and explicitly authorizes status/cleanup steps.
integrate:
	$(WORKSPACE) integrate $(INTEGRATE_ARGS)

build: workspace-check
	$(WORKSPACE) run $(GO_OUTPUT) build $(GO_BUILD_FLAGS) @workspace

build-binaries: webui
	$(MAKE) --no-print-directory build
	$(GO_OUTPUT) build $(GO_BUILD_FLAGS) -ldflags='$(BUILD_LDFLAGS)' -o '$(BUILD_OUTPUT)/' ./apps/mekugi/cmd/mekugi ./apps/mekugi/cmd/mekugi-exec
	$(MAKE) --no-print-directory build-native NATIVE_OUTPUT='$(BUILD_OUTPUT)'

build-native:
	python3 runtime/build.py build --profile '$(NATIVE_PROFILE)' --output '$(NATIVE_OUTPUT)'

check-native:
	$(TEST_ENV) python3 runtime/build.py check --profile '$(NATIVE_PROFILE)' -- $(NATIVE_ARGS)

test-native:
	$(TEST_ENV) python3 runtime/build.py test --profile '$(NATIVE_PROFILE)' --output '$(NATIVE_OUTPUT)' $(if $(NATIVE_TEST_FILTER),--test-filter '$(value NATIVE_TEST_FILTER)') -- $(NATIVE_ARGS)

package-native:
	python3 runtime/build.py package --profile '$(NATIVE_PROFILE)' --output '$(NATIVE_OUTPUT)' --archive '$(NATIVE_ARCHIVE)'

# Browser assets embedded by `mekugi web`. Go tests and plain Go builds work
# without Bun or Node.js; an unbundled UI host reports how to build its assets.
WEBUI := apps/mekugi/internal/webui/app

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

# Real terminal preview with an isolated, scripted local Responses provider.
preview-native-ui:
	set -eu; preview_bin=$$(mktemp -d); trap 'rm -rf "$$preview_bin"' EXIT; \
	preview_welcome=$$(awk '\
		/^## / { if (preview) exit; preview = ($$0 == "## Preview the terminal UI") } \
		!preview { next } \
		/^($$|## |- )/ { if (line != "") print line; line = $$0; if (line == "") print ""; next } \
		{ sub(/^ +/, ""); line = line (line == "" ? "" : " ") $$0 } \
		END { if (line != "") print line }' docs/contributing/development.md); \
	test -n "$$preview_welcome"; \
	$(GO_OUTPUT) build $(GO_BUILD_FLAGS) \
		-ldflags="-X \"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession.previewWelcome=$$preview_welcome\"" \
		-o "$$preview_bin/" ./apps/mekugi/cmd/mekugi ./apps/mekugi/cmd/mekugi-exec; \
	env -u BASH_ENV -u MEKUGI_EXEC_TRACK PATH='$(abspath $(NATIVE_OUTPUT))':"$$PATH" \
		MEKUGI_PREVIEW_BINARY="$$preview_bin/mekugi" $(GO) test $(GO_BUILD_FLAGS) \
		./apps/mekugi/internal/launch -run '^TestNativeUIWalkthrough$$' -count=1 -timeout 0

# Select the changed owner without rebuilding assets or disabling Go's test cache.
test: workspace-check
	$(TEST_ENV) $(GO_TEST_RUN) $(GO_BUILD_FLAGS) $(TEST_PACKAGES) $(TEST_SUPPORT_PACKAGES) -run '$(value TEST_RUN)' -parallel=$(TEST_PARALLEL) $(value TEST_FLAGS)

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
	if ! available="$$($(TEST_ENV) $(GO) test $(GO_BUILD_FLAGS) -tags=codex_e2e ./apps/mekugi/internal/launch -list "$$pattern")"; then \
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
	$(TEST_ENV) $(GO_TEST_RUN) $(GO_BUILD_FLAGS) ./testsupport/uisnapshot/... $(SNAPSHOT_PACKAGES) -run 'TestSnapshot|$(value SNAPSHOT)' -count=1

# Replaces baselines for the selected tests; review candidates/diffs first.
update-ui-snapshots:
	$(TEST_ENV) MEKUGI_UPDATE_UI_SNAPSHOTS=1 $(GO_TEST_RUN) $(GO_BUILD_FLAGS) $(SNAPSHOT_PACKAGES) -run '$(value SNAPSHOT)' -count=1
