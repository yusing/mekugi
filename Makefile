GO ?= go

.PHONY: install uninstall preview-assets preview-native-ui test-ui-snapshots update-ui-snapshots

SNAPSHOT ?= ^TestUISnapshot

install: preview-assets
	$(GO) install ./cmd/mekugi ./cmd/mekugi-exec

uninstall:
	$(GO) clean -i ./cmd/mekugi ./cmd/mekugi-exec

preview-assets:
	bun install --cwd plugins --frozen-lockfile
	go generate ./internal/router/toolplugin

# Interactive native Main fixtures. No Codex process or model requests.
# Plays a fake session automatically; keys act as in the real UI (Ctrl-C clears,
# interrupts, then exits).
preview-native-ui:
	env -u BASH_ENV MEKUGI_NATIVE_UI_PREVIEW=1 $(GO) test ./internal/router -run '^TestNativeUIPreview$$' -count=1 -timeout 0

# Offline rendered-output regression checks. Mismatches leave .txt.new candidates.
test-ui-snapshots:
	env -u BASH_ENV -u MEKUGI_UPDATE_UI_SNAPSHOTS $(GO) test ./internal/uisnapshot ./internal/ui/... ./internal/router -run 'TestSnapshot|$(SNAPSHOT)' -count=1

# Replaces baselines for the selected tests; review candidates/diffs first.
update-ui-snapshots:
	env -u BASH_ENV MEKUGI_UPDATE_UI_SNAPSHOTS=1 $(GO) test ./internal/ui/... ./internal/router -run '$(SNAPSHOT)' -count=1
