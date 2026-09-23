GO ?= go

.PHONY: install install-binaries uninstall uninstall-binaries

install: install-binaries

install-binaries:
	bun install --cwd plugins --frozen-lockfile
	go generate ./internal/router/toolplugin
	$(GO) install ./cmd/mekugi

uninstall: uninstall-binaries

uninstall-binaries:
	$(GO) clean -i ./cmd/mekugi
