# Code tests

Never use bare `make`, `make install`, `make install-binaries`, or an installation
build for validation. Build and test directly in the affected package.

## Focused checks

Go package rows use `go test <packages>`. Add `-count=1` when an uncached run
matters; measure its runtime separately from build/cache overhead.

| Changed owner | Focused check |
| --- | --- |
| Root review rendering | `.` |
| Router behavior | `./internal/router` |
| Live diff UI or streaming | `./internal/livediff` and `./internal/router -run 'Test.*LiveDiff'`, plus terminal acceptance below |
| Shared Go tokenizer | `./internal/tokenizer`, `./capturer`, `./internal/router/toolplugin` |
| Capture metrics and AX evidence | `./capturer` |
| Portable core or `mekugi:core/v1` adapter | `./internal/router/toolplugin`, then `./...` and `bun test ./internal/router/toolplugin/tests/core.test.ts` |
| TypeScript plugin source | `go generate ./internal/router/toolplugin`, then `bun test ./internal/router/toolplugin/tests` |
| Router process entry point | `./cmd/mekugi` |
| Configured frontend host acceptance | `-tags journal_e2e ./internal/router -run '^TestConfiguredToolFrontendNativeCodexE2E$'` (installed Codex, local mock provider) |
| Native mrun continuation acceptance | `-tags journal_e2e ./internal/router -run '^TestMRunNativeCodexYieldAndWriteStdinE2E$'` (installed Codex, local mock provider) |
| Native journal child-result acceptance | `-tags journal_e2e ./internal/router -run '^TestJournalNativeCodexSpawnE2E$'` (installed Codex, local mock provider) |
| Native post-compaction recovery | `-tags journal_e2e ./internal/router -run '^TestPostCompactNativeCodexE2E$'` (installed Codex, local mock provider) |
| Cross-package or broad contract | `./...` |

Generation requires Bun and dependencies declared in `plugins/package.json`. If
missing, use `bun install --cwd plugins --frozen-lockfile`. Generation rebuilds
the embedded WASM core and JavaScript bundle through directives in
`internal/router/toolplugin/runtime.go`; do not hand-edit generated assets.
Use a fresh temporary Bun transpiler cache when test discovery appears stale.

## Boundary coverage and test cost

Keep repeatable test costs visible. Reuse immutable registry fixtures while
isolating mutable thread, workspace, and process state. Startup and shutdown
tests still need their own owners. Use controlled time for in-process lifetimes.
Preserve real process-cleanup coverage and prove boundary coverage before
shrinking large fixtures.

Choose acceptance cases at the changed consumer:

- **Continuity:** test durable review/output/journal access after restart with
  only the requesting thread. Include affected fork, side-thread, agent-switch,
  model-switch, and resume paths. Live ancestry is not retained identity.
- **Launcher handoff:** validate cancellation and rendering before and after
  Codex owns the terminal, including redirected output and delayed startup.
- **Live diff:** test layout, viewport/follow state, and preview lifecycle
  independently. Run UI tests and real terminal (PTY) acceptance; inspect
  rendered frames, not only broker events. Streaming must show input before
  completion, continued following, independent diff scrolling, resize, and
  preview removal. Missing terminal coverage must be reported explicitly.
- **Stock edits:** check exact direct and Code Mode arguments/results, one
  host execution, streaming before completion, failed/partial outcomes,
  durable `mchanges` evidence, and dependent reads after persistence.
- **Execution:** cover Code Mode batching/parallelism, interpreter display,
  PTY/yield/`write_stdin`, bounded output, and `mread` recovery.
- **Producer shapes:** cover Chat versus Responses and persisted rollout events
  at the consuming boundary. Recheck dated host observations after Codex
  upgrades before relying on them.
- **Instruction projection:** check mixed authorization/tool text, fenced
  examples, and idempotence through stock, marked, custom, and multipart
  developer text parts.

The [interface specifications](doc/spec/index.md),
[ownership contracts](doc/architecture/index.md), and
[dated Codex observations](doc/codex-router-e2e.md) own the behavior and evidence.

## Temporary builds

On a non-main branch, after implementation and validation, prepare a runnable
temporary build outside the repository with `mktemp -d`. Include a launcher and
provide the exact command to test it. Do not install binaries for this step.
