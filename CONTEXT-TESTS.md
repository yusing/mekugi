# Code tests

## Focused checks

Inside an active Mekugi shell, run tests with invocation-local
`env -u BASH_ENV -u MEKUGI_EXEC_TRACK`.
The session's Bash startup hook can otherwise prepend live frontends ahead of a test's
isolated frontend PATH, sending fixture reads to the wrong retained-output store.
The inherited tracking guard also makes isolated command shells skip reporting.

`make test` applies that isolation and clears snapshot-update mode. Select packages
and tests while iterating, for example:

```sh
make test TEST_PACKAGES=./internal/router TEST_RUN='^TestShellRunnerMRun'
make test TEST_PACKAGES='./internal/appserver ./internal/router' TEST_RUN=AppServer
```

The default is `./...`. Unchanged successful tests can use Go's test cache; use
`TEST_FLAGS='-count=1'` for a fresh run, `TEST_FLAGS='-count=3 -shuffle=on'` for
repeat/isolation checks, or `TEST_FLAGS=-race` when checking concurrency. The recipe
does not regenerate assets or install binaries. Prepare missing assets once with
`make preview-assets`, and regenerate when their sources change as described below.
A focused pass does not cover unselected tests.

The release workflow runs the full fresh offline Go suite on Linux amd64;
other platforms retain command and welcome/version checks. Tagged Codex and
Bun plugin-host checks remain separate acceptance routes below.
The full suite includes configured-plugin fixtures, so it requires Node.js 24+
and ripgrep even when the tested application uses only built-in frontends.

`TEST_PARALLEL` defaults to 32 so independent process and PTY fixtures can overlap
their waits; override it for a constrained machine. Tests that change process-wide
environment or working directory remain serial. When full-suite timing is needed,
use `make test TEST_FLAGS=-count=1` and reuse warm build caches where available;
report compilation time separately from test execution.

For router profiling, use a temporary output directory and pass `-cpuprofile`,
`-blockprofile`, and `-o` paths through `TEST_FLAGS`. Inspect both CPU and block
profiles with `go tool pprof`; aggregate blocked goroutine time is not wall time.

| Changed owner | Focused check |
| --- | --- |
| Root review rendering | `.` |
| Activity rendering, diff navigation/previews, terminal input, or session metrics dialog | `./internal/ui/...`, plus affected router integration tests |
| Codex app-server RPC transport and process lifecycle | `./internal/appserver` and `./internal/router -run AppServer` |
| Router behavior | `./internal/router` |
| Journal MCP registration, schema, and authenticated reads | `./internal/router ./cmd/mekugi -run '^TestJournalMCP'`; installed-host check below |
| Live diff UI or streaming | `./internal/livediff` and `./internal/router -run 'Test.*LiveDiff'`, plus terminal acceptance below |
| Terminal layout, Activity, or Agents | `./internal/router -run 'Roster\|LiveActivity\|TerminalUI\|AppServer\|NativeUI'`; `make preview-native-ui` replays synthetic app-server events through the UI without model requests |
| Shared Go tokenizer | `./internal/tokenizer`, `./capturer`, `./internal/router/toolplugin` |
| Capture metrics and AX evidence | `./capturer` |
| Portable core or `mekugi:core/v1` adapter | `./internal/router/toolplugin`, then `./...` and `bun test ./internal/router/toolplugin/tests/core.test.ts` |
| Native frontends and output formatting | `./internal/router/toolplugin`, then `./internal/router` |
| Configured JavaScript plugin host | `./internal/router/toolplugin`, then `bun test ./internal/router/toolplugin/tests` |
| Router process entry point | `./cmd/mekugi` |
| Configured frontend host acceptance | `-tags journal_e2e ./internal/router -run '^TestConfiguredToolFrontendNativeCodexE2E$'` (installed Codex, local mock provider) |
| Native mrun continuation acceptance | `-tags journal_e2e ./internal/router -run '^TestMRunNativeCodexYieldAndWriteStdinE2E$'` (installed Codex, local mock provider) |
| Native journal child-result acceptance | `-tags journal_e2e ./internal/router -run '^TestJournalNativeCodexSpawnE2E$'` (installed Codex, local mock provider) |
| Native journal MCP read acceptance | `-tags journal_e2e ./internal/router -run '^TestJournalMCPNativeCodexReadE2E$'` (installed Codex, local mock provider) |
| Native journal host-result completion | `-tags journal_e2e ./internal/router -run '^TestJournalHostFinishNativeCodexSpawnE2E$'` (installed Codex, local mock provider) |
| Native post-compaction recovery | `-tags journal_e2e ./internal/router -run '^TestPostCompactNativeCodexE2E$'` (installed Codex, local mock provider) |
| Cross-package or broad contract | `./...` |

Native reader latency checks include a fresh authenticated worker, a 3,000-row
read, the three-document architecture batch, and an eight-file Go inspection at
the default shared budget. Run them without installing a binary:

```sh
make test TEST_PACKAGES=./internal/router TEST_RUN='^$' TEST_FLAGS='-bench=BenchmarkNativeFrontends -benchtime=3x -count=1'
```

The inspection batch intentionally exercises incomplete-output recovery. These
benchmarks clear the thread identity and exclude active-session lease/retention
costs. `BenchmarkSelectRows` in `./internal/tokenizer` isolates row admission from
process startup and source I/O.

Go symbol checks require an existing `gopls` and install nothing. Opt in to real
cross-package/test reference and source-freshness acceptance with:

```sh
env MEKUGI_TEST_REAL_GOPLS=1 make test TEST_PACKAGES=./internal/router/toolplugin TEST_RUN='^TestNativeSymbolRealGoplsReferences$'
```

Fresh-process resolver and authenticated frontend benchmarks use normal caches;
the frontend measurements exclude registry creation and active-session leases:

```sh
make test TEST_PACKAGES='./internal/router/toolplugin ./internal/router' TEST_RUN='^$' TEST_FLAGS='-bench=BenchmarkNativeSymbol -benchtime=5x -count=1'
```

Generation uses Go to rebuild the embedded WASM core for configured JavaScript plugins
through the directive in `internal/router/toolplugin/runtime.go`. Built-in frontends
and output formatting are compiled Go and require no generated JavaScript assets or
npm dependencies. Bun is needed only for the remaining plugin-host/shared-core tests.
Use a fresh temporary Bun transpiler cache when test discovery appears stale.

Journal/frontend prose has a separate generated consumer. Edit
`guidance/frontend_guidance.md.tmpl` or the executable helper's description owner,
then regenerate and check the prepared-request projection:

```sh
env MEKUGI_UPDATE_FRONTEND_GUIDANCE=1 make test TEST_PACKAGES=./internal/router TEST_RUN='^TestGeneratedFrontendGuidanceIsCurrent$'
make test TEST_PACKAGES=./internal/router TEST_RUN='GeneratedFrontendGuidance|ProjectedStockGuidance|JournalRulesHaveOneOwner|JournalGuidanceUsesRequestRole|Instruction|ConflictRewrite|WebSocketPrewarmToolGuidance'
```

## Static checks

Run `make lint` with `golangci-lint` and `deadcode` on PATH. It uses `.golangci.yml`
and includes test executables in deadcode analysis. Tool errors and reported findings
fail the target. Linux amd64 CI installs the versions pinned in the release workflow.

## Terminal UI snapshots

`make test-ui-snapshots` runs offline rendered-output regression tests without Codex
or model requests. Select a case with `SNAPSHOT='^TestUISnapshotJournalReply$'`.
Set `SNAPSHOT_PACKAGES=./internal/router` (or another owning package) to avoid
compiling and testing unrelated UI packages; the check still includes the snapshot
harness tests. The same package selection applies to `update-ui-snapshots`.
The supplied journal preview lives in
`internal/router/testdata/snapshots/journal-ui-preview.txt`. Router fixtures also cover
journal panes, cards, details, skills and file pickers, Main's composer, resume
sessions, the Agents roster, and responsive Activity layouts. Activity blocks and
output dialogs live in `internal/ui/activity/testdata/snapshots/`; diff navigation,
change graphs, and streaming previews live in `internal/ui/diffview/testdata/snapshots/`.
Launcher debug handoffs live in `cmd/mekugi/testdata/snapshots/`.
Shared Diff syntax and row styles live in `internal/livediff/testdata/snapshots/`.
Other owners keep fixtures in their own `testdata/snapshots/` directories.

`internal/uisnapshot.Assert` strips ANSI sequences only: spacing, blank lines,
wrapping, and borders remain exact. Tests fix time, theme, dimensions, and other
nondeterministic inputs before invoking the actual renderer. Text snapshots do
not establish color/style correctness. `internal/uisnapshot.AssertTerminal`
paints actual renderer rows into the shared VT emulator at a fixed width, then
stores its canonical ANSI output as quoted rows. These fixtures cover terminal
cell colors, emphasis, links, padding, and style restoration without depending on
the renderer's choice of equivalent escape sequences. Include a following plain
row when checking that a style does not leak. Activity code fills, tables,
reasoning, wait targets, and answer flashes, shared Diff syntax and row fills,
and native dialog, attachment, keybinding, and roster surfaces extend the
terminal-style coverage. Theme-sensitive cases cover terminal, dark, and light
themes; code fills also cover a reported background.
Both snapshot forms use the same review/update commands and preserve separate
copy, state, bounds, interaction, and PTY acceptance.
Repository Git attributes suppress trailing-space and final-blank-row warnings
only for these text fixtures; `git diff --check` still checks ordinary sources.

A missing or changed fixture fails the test and writes a sibling `.txt.new`
candidate with a unified diff in the failure output. The reviewed fixture stays
unchanged. Inspect the candidate, then either move that candidate over its fixture
or run `make update-ui-snapshots SNAPSHOT='^TestUISnapshotJournalReply$'` to
regenerate the selected baseline. Omitting `SNAPSHOT` updates all matching cases.
Updates are opt-in via `MEKUGI_UPDATE_UI_SNAPSHOTS=1`; the check recipe clears it
so inherited configuration cannot silently accept changes. A passing comparison
or explicit update removes its stale candidate. Rerun the check after acceptance.

## Boundary coverage and test cost

Keep repeatable test costs visible. Reuse immutable registry fixtures while
isolating mutable thread, workspace, and process state. Startup and shutdown
tests still need their own owners. Disposable Git fixtures must isolate system
and global configuration so setup does not invoke personal signing programs or
hooks; keep repository-local settings for filter and worktree boundary tests.
Resolve fixture shells with `execTrackShellExecutable` in `internal/router/exec_track_test.go`:
`exec.Command` resolves names before `Cmd.Env` is set, so an isolated environment alone
can still select the live session's approval guard. Keep fixture tools first for nested shells.
Use controlled time for in-process lifetimes, including retention retries and
filesystem-lock contention. Keep subtests and parallel scheduling outside each
`synctest` bubble, and create and clean up its workers inside it.
Preserve real process-cleanup coverage and prove boundary coverage before
shrinking large fixtures.

The following acceptance routes apply only when the requested change touches their contract:

- **Continuity:** for changed durable review/output/journal state, test restart with only the
  requesting thread and affected branch/switch/resume paths. Live ancestry is not retained identity.
- **Launcher handoff:** validate cancellation and rendering before and after
  Codex owns the terminal, including redirected output and delayed startup.
- **Live diff:** test layout, viewport/follow state, and preview lifecycle
  independently. Run UI tests and real terminal (PTY) acceptance; inspect
  rendered frames, not only broker events. Streaming must show input before
  completion, continued following, independent diff scrolling, resize, and
  preview removal. Missing terminal coverage must be reported explicitly.
- **Stock edits:** check exact nested stock-tool arguments/results, one
  host execution, streaming before completion, failed/partial outcomes,
  durable `mchanges` evidence, and dependent reads after persistence.
- **Execution:** cover exec batching/parallelism, interpreter display,
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
