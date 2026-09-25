GO ?= go

.PHONY: install install-binaries uninstall uninstall-binaries preview-assets preview-ui preview-roster

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

# Interactive: replays a scripted session in the real wrapped UI. Ctrl-B 1, then Ctrl-D, quits.
# MEKUGI_UI_PREVIEW_STEP sets the replay pace (default 1.2s).
preview-ui: preview-assets
	env -u BASH_ENV MEKUGI_UI_PREVIEW=1 $(GO) test ./internal/router -run '^TestTerminalUIPreview$$' -count=1 -timeout 0

# Headless: replays the same session and prints each changed roster frame with
# the canonical usage report behind every agent. Optional: PREVIEW_WIDTH (160),
# PREVIEW_ROWS (8), PREVIEW_STEP (20ms), PREVIEW_ANSI=1 to keep colors.
preview-roster: preview-assets
	env -u BASH_ENV MEKUGI_UI_PREVIEW_FRAMES=1 MEKUGI_UI_PREVIEW_WIDTH=$(PREVIEW_WIDTH) MEKUGI_UI_PREVIEW_ROWS=$(PREVIEW_ROWS) \
		MEKUGI_UI_PREVIEW_STEP=$(PREVIEW_STEP) MEKUGI_UI_PREVIEW_ANSI=$(PREVIEW_ANSI) \
		$(GO) test ./internal/router -run '^TestTerminalUIPreviewFrames$$' -count=1 -v
