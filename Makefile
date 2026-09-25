GO ?= go

.PHONY: install install-binaries uninstall uninstall-binaries preview-ui

install: install-binaries

install-binaries:
	bun install --cwd plugins --frozen-lockfile
	go generate ./internal/router/toolplugin
	$(GO) install ./cmd/mekugi

uninstall: uninstall-binaries

uninstall-binaries:
	$(GO) clean -i ./cmd/mekugi

# Interactive: replays a scripted session in the real wrapped UI. Ctrl-B 1, then Ctrl-D, quits.
# MEKUGI_UI_PREVIEW_STEP sets the replay pace (default 1.2s).
preview-ui:
	bun install --cwd plugins --frozen-lockfile
	go generate ./internal/router/toolplugin
	env -u BASH_ENV MEKUGI_UI_PREVIEW=1 $(GO) test ./internal/router -run '^TestTerminalUIPreview$$' -count=1 -timeout 0
