# Testing

Load a specialized guide only when that operation applies:

| Operation | Guide |
| --- | --- |
| Native Rust runtime checks/tests | `runtime/codex/AGENTS.md#validation-and-generation` |
| Installed-Codex `codex_e2e` fixtures (local mock providers) | `docs/contributing/testing-host.md` |
| Live-provider `e2e` fixtures | `docs/contributing/testing-live-codex.md` |
| Terminal UI snapshots | `docs/contributing/testing-snapshots.md` |
| Browser control transport fixture & wire types | `docs/contributing/testing-browser-control.md` |
| Benchmarks and test CPU/block profiles | `docs/contributing/testing-performance.md` |

## Runner

Use `make test` from the repository root (root has no Go module):

```sh
make test TEST_PACKAGES='./backends/codex/appserver ./apps/mekugi/internal/launch' TEST_RUN=AppServer
```

- `TEST_PACKAGES` defaults to `@workspace` (all `go.work` members); `TEST_RUN` and `TEST_FLAGS` pass through to `go test`. Set `RTK=` for raw Go output.
- `TEST_PARALLEL` (default 32) overlaps process/PTY waits; keep env/cwd-mutating tests serial.
- For raw `go test` inside an active Mekugi shell, run `env -u BASH_ENV -u MEKUGI_EXEC_TRACK` so live hooks/frontends do not intercept fixtures (`make test` already clears them).
- Tests neither generate assets nor install binaries. Interpreter/query fixtures need Node.js and `ripgrep`.
- CI runs the offline suite on Linux amd64; other platforms run command/version checks.
- Report wall time for full runs (cold build/race/live separately); do not rerun passing tests to chase the ~30s warm-cache target.

## Coverage & static checks

- **Native vs Go E2E**: when moving behavior to `runtime/codex` or adding a native-binary Go E2E test, check Rust coverage first. Put native behavior tests in Rust and keep Go E2E only for distinct Go-side integration.
- **Static checks**:
  - `make docs-check`: validates Markdown links and heading anchors (also run by pre-commit hook).
  - `make lint`: `golangci-lint` + `deadcode` (run before committing Go changes).
  - `make check-generated`: verifies `docs/reference/modules.md` and browser Session API types (`go run ./apps/mekugi/cmd/workspace index-write` / `go run ./apps/mekugi/cmd/sessionapi-schema` regenerate individually).
  - `make preview-native-ui`: runs real Mekugi against a scripted local provider (see [Preview the terminal UI](docs/contributing/development.md#preview-the-terminal-ui)).
- **Opt-in focused checks** (never install missing tools):
  - Real `gopls`: `env MEKUGI_TEST_REAL_GOPLS=1 make test TEST_PACKAGES=./modules/symbolquery/query TEST_RUN='^TestNativeSymbolRealGoplsReferences$'`
  - Real `rust-analyzer`: `env MEKUGI_TEST_REAL_RUST_ANALYZER=1 make test TEST_PACKAGES='./modules/symbolquery/query ./apps/mekugi/internal/launch' TEST_RUN='Rust'`
  - Native Rust checkout cross-crate query: `env MEKUGI_TEST_RUST_RUNTIME=1 make test TEST_PACKAGES=./apps/mekugi/internal/launch TEST_RUN='^TestMSymbolRustRuntimeCheckout$' TEST_FLAGS=-v`
  - Helper guidance (native + wrapped launcher): `make test-native NATIVE_ARGS='-p codex-core --lib -- mekugi_guidance::tests'` and `make test TEST_PACKAGES='./apps/mekugi/internal/frontends ./apps/mekugi/internal/launch' TEST_RUN='Guidance|ComposedFrontendCatalog|ToolFrontend'`
- After API changes, compile tagged host/live fixtures (`TEST_RUN='^$'` with `-tags=codex_e2e` / `-tags=codex_e2e,e2e`).

## Fixture gotchas

- Isolate mutable thread/workspace/process state; reuse immutable registries. Disposable Git fixtures must isolate global/system config to avoid personal hooks/signing.
- Resolve fixture shells via `execTrackShellExecutable` (`apps/mekugi/internal/launch/exec_track_test.go`): `exec.Command` resolves executables before `Cmd.Env`, so env isolation alone can hit the live approval guard.
- Use controlled time for lifetimes/locks; keep subtests and parallel scheduling outside `synctest` bubbles (create/clean up workers inside).
