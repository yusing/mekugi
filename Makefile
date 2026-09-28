GO ?= go

.PHONY: install uninstall preview-assets preview-native-ui

install:
	bun install --cwd plugins --frozen-lockfile
	go generate ./internal/router/toolplugin
	$(GO) install ./cmd/mekugi

uninstall:
	$(GO) clean -i ./cmd/mekugi

preview-assets:
	bun install --cwd plugins --frozen-lockfile
	go generate ./internal/router/toolplugin

# Interactive native Main fixtures. No Codex process or model requests.
# Plays a fake session automatically; keys act as in the real UI (Ctrl-C clears,
# interrupts, then exits).
preview-native-ui:
	env -u BASH_ENV MEKUGI_NATIVE_UI_PREVIEW=1 $(GO) test ./internal/router -run '^TestNativeUIPreview$$' -count=1 -timeout 0
