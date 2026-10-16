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
make test TEST_PACKAGES='./backends/codex/appserver ./internal/router' TEST_RUN=AppServer
```

The default is `@workspace`, expanded from the explicit members in `go.work`.
Focused selections also include the shared snapshot harness in
`testsupport/uisnapshot`. Root `./...` alone excludes nested modules.
Unchanged successful tests can use Go's test cache; use
`TEST_FLAGS='-count=1'` for a fresh run, `TEST_FLAGS='-count=3 -shuffle=on'` for
repeat/isolation checks, or `TEST_FLAGS=-race` when checking concurrency. The recipe
does not regenerate assets or install binaries. A focused pass does not cover unselected tests.

The release workflow runs the full fresh offline Go suite on Linux amd64;
other platforms retain command and welcome/version checks. Tagged Codex checks remain separate acceptance routes below. `make test-host` runs
the focused installed-Codex set described in `CONTEXT-AUTOMATED-TESTS.md`.
Interpreter and query fixtures use Node.js and ripgrep. Native frontends and
their catalog require no JavaScript runtime.

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
| Captured diff model, composition, merge and status | `./libraries/diffmodel/...`, plus affected router and terminal consumers |
| Activity rendering, diff navigation/previews, terminal input, or session metrics dialog | `./internal/ui/...`, plus affected router integration tests |
| Codex app-server RPC transport and process lifecycle | `./backends/codex/appserver` and `./internal/router -run AppServer` |
| Host-selected rollout evidence | `./backends/codex/rollout`, plus router context, resume-settings, and restored-Activity consumers |
| Router behavior | `./internal/router` |
| Session publication, namespaces, leases, and retention | `./modules/sessionhistory/...`, plus affected router persistence/fork/recovery integration |
| Command-segment report transport and retained reports | `./modules/commandsegments/...`, plus router `ExecTrack`, `CommandSegments`, `StorageInspection`, timing and restored-activity consumers |
| Composed approval policy and grants | `./modules/remotewriteapproval/... ./modules/sudoapproval/... ./backends/codex/binding ./apps/mekugi/internal/composition`, plus launcher `SelectedGuards` and router `ShellApprovalComposition`, `NativeApprovalGuard`, `Sudo` consumers |
| Shared shell instrumentation | `./libraries/shellinstrumentation/... ./libraries/commandanalysis/...`, plus router `ExecTrack`, `VCSGuard`, `Sudo` and guard-display consumers |
| Shell/source execution-scope classification | `./libraries/commandanalysis/... ./libraries/sourceanalysis/sourceparse ./internal/execscope`, plus router observation, native trace, preview, and display tests |
| Provider credentials and model metadata | `./providers/grok/... ./providers/opencode/...`, plus router and launcher `Grok`, `OpenCode`, `ThirdParty`, and accounting consumers |
| Provider request encoding and stream normalization | `./backends/codex/responses ./libraries/modelprotocol/...`, plus router Grok/OpenCode integration and capture tests |
| Optional session naming | `./modules/sessionnaming/...`, plus router `SessionTitle`, `NativeSessionTitle`, `TerminalTitle`, and submitted-assignment consumers; installed persistence/resume cases in `app_server_session_title_codex_e2e_test.go` |
| Inactive instruction loading | `./modules/contextreset/...`, plus router selected-skill acquisition and attachment consumers |
| Reserved MCP transport | `./libraries/mcptransport`, plus application worker/bridge dispatch and tagged guard-outage fixtures |
| Shell-hook helper, registration, and effective configuration | `./apps/mekugi/cmd/mekugi ./apps/mekugi/cmd/mekugi-exec ./backends/codex/shellhook ./modules/remotewriteapproval/... ./modules/sudoapproval/... ./backends/codex/shellapproval ./internal/router -run 'TestGuardDiscovery\|TestGuardHook\|TestHookConfig\|TestRewriteHook\|TestOptionalTrackingHookArgs\|TestValidateGuardHook\|TestAppServerGuardHook'`; installed-host guard/fault checks in `CONTEXT-AUTOMATED-TESTS.md` |
| Live diff UI or streaming | `./internal/livediff` and `./internal/router -run 'Test.*LiveDiff'`, plus terminal acceptance below |
| Terminal layout, Activity, or Agents | `./internal/router -run 'Roster\|LiveActivity\|TerminalUI\|AppServer\|NativeUI'`; `make preview-native-ui` replays synthetic app-server events through the UI without model requests |
| Shared Go tokenizer | `./libraries/outputbudget/tokenizer`, `./capturer` |
| Duplicate-output projection and submitted attachment codec | `./modules/outputdeduplication/... ./libraries/modelprotocol/attachments ./backends/codex/binding`, plus router duplicate-output, attachment, continuation, and provider-prefix consumers |
| Capture metrics and AX evidence | `./capturer` |
| Semantic symbol queries | `./modules/symbolquery/...`, plus router symbol/activity/AX and recovery consumers |
| Structural inspection | `./modules/sourceoutline/... ./libraries/sourceanalysis/...`, then authenticated router frontend/recovery tests |
| Raw source reading | `./modules/fileread/...`, then router read/recovery/AX and activity consumers |
| Retained changes, frozen selections, and mutation | `./modules/changehistory/...`, plus router `MChanges`, `Changes`, retention/fork/recovery, and live-view consumers |
| Retained paging | `./modules/outputrecovery/...`, then router mread/storage/fork integration |
| Shared command options and output admission | `./libraries/commandargs/... ./libraries/outputbudget/...`, plus affected frontend integration |
| Bounded commands | `./modules/boundedcommand/...`, plus `./internal/router -run MRun` for authenticated delivery and process lifecycle |
| Router process entry point | `./apps/mekugi/cmd/mekugi` |
| Native frontend host acceptance | `-tags codex_e2e ./internal/router -run '^TestNativeToolFrontendCodexE2E$'` (installed Codex, local mock provider) |
| Native mrun continuation acceptance | `-tags codex_e2e ./internal/router -run '^TestMRunNativeCodexYieldAndWriteStdinE2E$'` (installed Codex, local mock provider) |
| Cross-package or broad contract | `@workspace` |

Native reader latency checks include a fresh authenticated worker, a 3,000-row
read, the three-document architecture batch, and an eight-file Go inspection at
the default shared budget. Run them without installing a binary:

```sh
make test TEST_PACKAGES=./internal/router TEST_RUN='^$' TEST_FLAGS='-bench=BenchmarkNativeFrontends -benchtime=3x -count=1'
```

The inspection batch intentionally exercises incomplete-output recovery. These
benchmarks clear the thread identity and exclude active-session lease/retention
costs. `BenchmarkSelectRows` in `./libraries/outputbudget/tokenizer` isolates row admission from
process startup and source I/O.

Go symbol checks require an existing `gopls` and install nothing. Opt in to real
cross-package/test reference and source-freshness acceptance with:

```sh
env MEKUGI_TEST_REAL_GOPLS=1 make test TEST_PACKAGES=./modules/symbolquery/query TEST_RUN='^TestNativeSymbolRealGoplsReferences$'
```

Fresh-process resolver and authenticated frontend benchmarks use normal caches;
the frontend measurements exclude registry creation and active-session leases:

```sh
make test TEST_PACKAGES='./modules/symbolquery/query ./internal/router' TEST_RUN='^$' TEST_FLAGS='-bench=BenchmarkNativeSymbol -benchtime=5x -count=1'
```

Frontend descriptions and supplementary `guidance.md` live with their command
modules. `modules/agentguidance` renders the selected invocation catalog directly;
its Codex binding owns the shared layout and instruction projection. Check the
actual authenticated snapshot and prepared-request projection after prose changes:

```sh
make test TEST_PACKAGES='./modules/agentguidance/... ./internal/router ./apps/mekugi/internal/composition' TEST_RUN='Guidance|Instruction|ConflictRewrite|Prewarm|ComposedFrontendCatalog'
```

## Static checks

Run `make lint` with `golangci-lint` and `deadcode` on PATH. It uses `.golangci.yml`
and checks production reachability separately from test reachability, including
the shared snapshot harness tests. Tool errors and reported findings
fail the target. Linux amd64 CI installs the versions pinned in the release workflow.

## Terminal UI snapshots

Enable the staged-tree snapshot commit gate as described in
[Development](docs/contributing/development.md#build-and-test).

`make test-ui-snapshots` runs offline rendered-output regression tests without Codex
or model requests. Select a case with `SNAPSHOT='^TestUISnapshotNativeSessionShortcuts$'`.
Set `SNAPSHOT_PACKAGES=./internal/router` (or another owning package) to avoid
compiling and testing unrelated UI packages; the check still includes the snapshot
harness tests. The same package selection applies to `update-ui-snapshots`.
Router fixtures cover
skills and file pickers, Main's composer, resume
sessions, the Agents roster, and responsive Activity layouts. Activity blocks and
output dialogs live in `internal/ui/activity/testdata/snapshots/`; diff navigation,
change graphs, and streaming previews live in `internal/ui/diffview/testdata/snapshots/`.
Launcher debug handoffs live in `apps/mekugi/cmd/mekugi/testdata/snapshots/`.
Shared Diff syntax and row styles live in `internal/livediff/testdata/snapshots/`.
Other owners keep fixtures in their own `testdata/snapshots/` directories.

`testsupport/uisnapshot.Assert` strips ANSI sequences only: spacing, blank lines,
wrapping, and borders remain exact. Tests fix time, theme, dimensions, and other
nondeterministic inputs before invoking the actual renderer. Text snapshots do
not establish color/style correctness. `testsupport/uisnapshot.AssertTerminal`
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
or run `make update-ui-snapshots SNAPSHOT='^TestUISnapshotNativeSessionShortcuts$'` to
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

- **Continuity:** for changed durable review/output state, test restart with only the
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
