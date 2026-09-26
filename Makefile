GO ?= go

.PHONY: install install-binaries uninstall uninstall-binaries preview-assets preview-native-ui

install: install-binaries

install-binaries:
	bun install --cwd plugins --frozen-lockfile
	go generate ./internal/router/toolplugin
	$(GO) install ./cmd/mekugi

uninstall: uninstall-binaries

uninstall-binaries:
	$(GO) clean -i ./cmd/mekugi

preview-assets:
	bun install --cwd plugins --frozen-lockfile
	go generate ./internal/router/toolplugin

# Interactive native Main fixtures. No Codex process or model requests.
# Plays a fake session automatically; Ctrl-C or Ctrl-D exits.
preview-native-ui:
	env -u BASH_ENV MEKUGI_NATIVE_UI_PREVIEW=1 $(GO) test ./internal/router -run '^TestNativeUIPreview$$' -count=1 -timeout 0
