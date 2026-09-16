# Code tests

## Focused checks

Go package rows use `go test <packages>`. `-count=1` requests an uncached execution;
measure that separately from build/cache overhead.

| Changed owner | Focused check |
| --- | --- |
| Root engine | `.` |
| Router behavior | `./internal/router` |
| Live diff UI or streaming behavior | `./internal/router -run 'Test.*LiveDiff'` plus the terminal acceptance cases below |
| Capture metrics and AX evidence | `./capturer` |
| Portable core or `mekugi:core/v1` adapter | `./internal/router/toolplugin`, then `./...` and `bun test ./internal/router/toolplugin/tests/core.test.ts` |
| TypeScript plugin source | `go generate ./internal/router/toolplugin`, then `bun test ./internal/router/toolplugin/tests` |
| Router or shell-helper process entry point | `./cmd/mekugi ./cmd/shell` |
| Native journal child-result acceptance | `-tags journal_e2e ./internal/router -run '^Test(Shell)?JournalNativeCodexSpawnE2E$'` (installed Codex, local mock provider) |
| Cross-package or broad contract | `./...` |

Generation requires Bun and the dependencies declared in `plugins/package.json`. If those
dependencies are missing, use `bun install --cwd plugins --frozen-lockfile`. Generation rebuilds
the embedded WASM core and JavaScript bundle through directives in
`internal/router/toolplugin/runtime.go`; do not hand-edit generated assets.

## Boundary coverage and test cost

Keep repeatable test costs visible. Reuse immutable registry fixtures while isolating mutable
thread, workspace, and process state; startup and shutdown tests still need their own owners.
Use controlled time for in-process lifetimes. Preserve real process-cleanup coverage and prove
boundary coverage before shrinking large fixtures. Historical benchmark artifacts must stay
outside root Go package discovery without deleting their evidence.

Choose acceptance cases at the changed consumer:
- **Continuity:** test relative journal access after restarting with only the requesting thread.
  Live ancestry is not a substitute for retained identity and conflict checks. Include affected
  fork, side-thread, agent-switch, model-switch, and resume paths.
- **Launcher handoff:** validate cancellation and rendering before and after Codex owns the
  terminal, including redirected output and delayed initialization.
- **Live diff:** keep layout, viewport/follow state, and preview lifecycle independently
  testable. For live diff changes, run the affected UI tests and real terminal (PTY)
  acceptance tests, and inspect rendered frames, not only broker events or source.
  Streaming changes must demonstrate visible updates before input completion, following
  beyond the viewport, independent captured-diff scrolling, resize behavior, and delayed
  preview removal with restored diff height. Cover interruption and burst updates without
  replaying stale frames. Use controlled time for state tests and measure render/update
  cost when changing performance. Missing terminal coverage must be reported explicitly.
- **Verified edits:** feed emitted references through the edit consumer with repeated source
  rows. Formatter-only assertions cannot prove that the intended row was selected.
- **Producer shapes:** cover Chat versus Responses, multipart CTP, and persisted rollout events
  at the consuming boundary. Synthetic start events cannot establish persisted timing coverage.
  Recheck dated host observations after Codex upgrades before relying on them.
- **Instruction rewriting:** check mixed authorization/tool text, fenced examples, and idempotence
  through stock, marked, custom, and each multipart developer text part.

The [interface specifications](doc/spec/index.md), [ownership contracts](doc/architecture/index.md),
and [dated Codex observations](doc/codex-router-e2e.md) own the corresponding behavior and evidence.

## Temporary builds

After implementing and validating a feature, always prepare a runnable temporary build outside
the repository using `mktemp -d`. Include required helpers and a launcher, and provide the exact
command to test it.
