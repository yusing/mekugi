# Installed-Codex host acceptance

`codex_e2e`-tagged fixtures exercise installed Codex and `mekugi-app-server` against deterministic
local mock providers with fake credentials and isolated home/configuration (never live models).

Build native binaries (`mekugi-app-server`, `mekugi-models.json`) with `make build-native` and put
`bin/native` on `PATH`. Remaining stock-CLI fixtures also need `codex` on `PATH`. Raw `go test`
invocations for compaction fixtures must set `MEKUGI_E2E_MODEL` (set by `Makefile`).

## Running fixtures

Run the focused host suite (authenticated frontends, PTY yield/`write_stdin`, nested `mchanges`,
and Linux sandbox segment reports):

```sh
make test-host
```

Run targeted launch or hook-discovery fixtures:

```sh
make test TEST_PACKAGES=./apps/mekugi/internal/launch TEST_RUN='^(TestAppServerVCSGuardNativeCodexYolo|TestAppServerGuardTransportFailureNativeCodex)$' TEST_FLAGS='-tags=codex_e2e -count=1'
make test TEST_PACKAGES=./apps/mekugi/internal/launch TEST_RUN='^TestAppServerExecTrackNativeCodex(Sandbox)?$' TEST_FLAGS='-tags=codex_e2e -count=1'
make test TEST_PACKAGES=./backends/codex/shellhook TEST_RUN='^TestGuardDiscoveryConfigurationNativeCodex$' TEST_FLAGS='-tags=codex_e2e -count=1'
```

Compile all tagged launch fixtures (`apps/mekugi/internal/launch/*_codex_e2e_test.go`) without
running them:

```sh
make test TEST_PACKAGES=./apps/mekugi/internal/launch TEST_RUN='^$' TEST_FLAGS='-tags=codex_e2e'
```

Report skips (such as Linux-only sandbox checks) as missing coverage. These local-mock fixtures do
not establish live-provider behavior; see [live Codex checks](testing-live-codex.md).
