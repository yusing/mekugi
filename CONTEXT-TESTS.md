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
environment or working directory remain serial. Measure the default suite with
`make test TEST_FLAGS=-count=1` after warming build caches; report compilation time
separately from test execution.

For router profiling, use a temporary output directory and pass `-cpuprofile`,
`-blockprofile`, and `-o` paths through `TEST_FLAGS`. Inspect both CPU and block
profiles with `go tool pprof`; aggregate blocked goroutine time is not wall time.

| Changed owner | Focused check |
| --- | --- |
| Root review rendering | `.` |
| Activity rendering, diff navigation/previews, terminal input, or session metrics dialog | `./internal/ui/...`, plus affected router integration tests |
| Codex app-server RPC transport and process lifecycle | `./internal/appserver` and `./internal/router -run AppServer` |
| Router behavior | `./internal/router` |
| Live diff UI or streaming | `./internal/livediff` and `./internal/router -run 'Test.*LiveDiff'`, plus terminal acceptance below |
| Terminal layout, Activity, or Agents | `./internal/router -run 'Roster\|LiveActivity\|TerminalUI\|AppServer\|NativeUI'`; `make preview-native-ui` replays synthetic app-server events through the UI without model requests |
| Shared Go tokenizer | `./internal/tokenizer`, `./capturer`, `./internal/router/toolplugin` |
| Capture metrics and AX evidence | `./capturer` |
| Portable core or `mekugi:core/v1` adapter | `./internal/router/toolplugin`, then `./...` and `bun test ./internal/router/toolplugin/tests/core.test.ts` |
| Native frontends and output formatting | `./internal/router/toolplugin`, then `./internal/router` |
| Configured JavaScript plugin host | `./internal/router/toolplugin`, then `bun test ./internal/router/toolplugin/tests` |
| Router process entry point | `./cmd/mekugi` |
| Claude native SDK usage and fresh-bridge resume | `MEKUGI_TEST_NATIVE_CLAUDE=1 make test TEST_PACKAGES=./internal/claude TEST_RUN='^TestClaudeNativeUsageAndFreshBridgeResume$' TEST_FLAGS='-count=1 -v'` after `make test-claude`; sends two real prompts through the installed authenticated Claude runtime, with its normal billing/configuration |
| Claude native image and file mention | `MEKUGI_TEST_NATIVE_CLAUDE=1 make test TEST_PACKAGES=./internal/claude TEST_RUN='^TestClaudeNativeImageAndFileMention$' TEST_FLAGS='-count=1 -v'` after `make test-claude`; sends one real image/mention prompt and permits native Read in an isolated fixture, with normal native billing/configuration |
| Claude native fork and parent isolation | `MEKUGI_TEST_NATIVE_CLAUDE=1 make test TEST_PACKAGES=./internal/claude TEST_RUN='^TestClaudeNativeForkKeepsParentConversation$' TEST_FLAGS='-count=1 -v'` after `make test-claude`; four real no-tool prompts establish distinct fork identity/context and fresh parent/fork-resume isolation, not UI-process or inherited-capture acceptance |
| Claude retaining user-shell shortcut | After `make test-claude`, run `MEKUGI_TEST_NATIVE_CLAUDE=1 make test TEST_PACKAGES=./internal/claude TEST_RUN='^TestClaudeNativeUserShell$' TEST_FLAGS='-count=1 -v -timeout=2m'` and the same command with `TEST_PACKAGES=./internal/router TEST_RUN='^TestNativeRuntimeShellClaudePTY$'`. Installed-native local-provider API and PTY cover retained context, fresh resume, fork isolation, nonzero output, exactly-once effects, shutdown/resume, drafts, model/effort and companion guidance. API also covers an active independent side and isolated follow-up after Main cancellation. No inference. Offline: `NativeRuntimeShell`, `NativeShellCarriers`, `UISnapshotNativeRuntimeShell`; bridge tests cover lost shutdown evidence. |
| Claude shared side-question dock | `MEKUGI_TEST_NATIVE_CLAUDE=1 make test TEST_PACKAGES=./internal/router TEST_RUN='^TestNativeRuntimeBTWClaudePTY$' TEST_FLAGS='-count=1 -v -timeout=3m'` after `make test-claude`; installed native SDK with a local scripted provider proves streamed shared `/btw`, isolated native context/follow-ups, concurrent Main, drafts, cancellation, new snapshots, file-picker contents, images, model inheritance, disabled tool effects and Main-only persistence. No inference; requested-effort runtime acceptance remains unverified. Offline state and rendered cases: `NativeRuntimeBTW`, `UISnapshotNativeRuntimeBTW`. |
| Claude native controls during streaming | `MEKUGI_TEST_NATIVE_CLAUDE=1 make test TEST_PACKAGES=./internal/claude TEST_RUN='^TestClaudeNativeModelSwitchDuringStreaming$' TEST_FLAGS='-count=1 -v'` after `make test-claude`; two real no-tool prompts establish controls sent after public text begins, matching native receipts and subsequent model output in native usage; effort acknowledgement is not proof of policy-limited effective effort |
| Claude shared session controls | After `make test-claude`, run `MEKUGI_TEST_NATIVE_CLAUDE=1 make test TEST_PACKAGES=./internal/router TEST_RUN='^TestNativeRuntimeSessionControlsClaude(Native\|PTY)$' TEST_FLAGS='-count=1 -v -timeout=3m'`. Installed native Claude with a local scripted provider proves native title persistence, empty clear, native history/context restoration and repeated A → B → A observation handoffs. Actual-loop PTY covers three-page picker search, Escape/Ctrl-C cancellation, Enter resume, failed-preflight draft retention and fresh title/history restoration before input, with exactly-once native shell effects. Cross-workspace Cwd/All filtering is covered by the acceptance below. No inference. Offline controller and rendered coverage: `NativeRuntimeControls`, `NativeRuntimeTitle`, `RuntimeSessionSwitch`, `UISnapshotNativeRuntimeResumePicker`, `UISnapshotNativeRuntimeRenamedTitle`. |
| Claude cross-workspace resume | `MEKUGI_TEST_NATIVE_CLAUDE=1 make test TEST_PACKAGES=./internal/router TEST_RUN='^TestNativeRuntimeCrossWorkspaceClaudeNative$' TEST_FLAGS='-count=1 -v -timeout=2m'` after `make test-claude`; installed native Claude with a local scripted provider proves A → B → A, paths with spaces, native title/history/model cwd, current guidance/frontends, failed-preflight draft retention, fresh A-launch B-resume and exactly-once Bash effects. Shared PTY acceptance covers All filtering, search and Enter in both directions. No inference; full session-control PTY coverage remains separate. |
| Claude native permissions and task stop | Offline exact command-row receipts and shared dialogs: `make test TEST_PACKAGES='./internal/claude ./internal/router' TEST_RUN='AdapterPermission\|UISnapshotNativeRuntimeCommandPermissions' TEST_FLAGS=-count=1`. SDK-shaped JSON-lines cover early/late pending, native-confirmed allow/deny, child isolation, cancellation and automatic denial. After `make test-claude`, run `MEKUGI_TEST_NATIVE_CLAUDE=1 make test TEST_PACKAGES=./internal/router TEST_RUN='^TestNativeRuntimePermissionsClaudeNative$' TEST_FLAGS='-count=1 -v -timeout=2m'` for installed-native scripted-provider command rows, open dialogs, exact IDs, explicit choices, cancellation, automatic denial and one allowed effect, without inference or PTY coverage. Native permission/task-stop API: `MEKUGI_TEST_NATIVE_CLAUDE=1 make test TEST_PACKAGES=./internal/claude TEST_RUN='^TestClaudeNative(PermissionDecisions\|BackgroundTaskStop)$' TEST_FLAGS='-count=1 -v'`; three real prompts with temporary project ask rules verify explicit allow/deny, exactly-once effects and native background stop acknowledgement/settlement |
| Claude direct child messages | `MEKUGI_TEST_NATIVE_CLAUDE=1 make test TEST_PACKAGES=./internal/claude TEST_RUN='^TestClaudeNativeDirectAgentMessages$' TEST_FLAGS='-count=1 -v -timeout=2m'` after `make test-claude`; installed-native local provider proves busy queue delivery, completed same-child continuation, fresh resume, unknown/foreign/fork-source rejection, a plain-text message above 256 KiB, exactly-once effects and explicit permission denial. No inference. Shared `/to` autocomplete and delivery acceptance uses `TestNativeRuntimeAgentsClaudePTY` below. Offline interaction and rendering: `NativeRuntimeMessages`, `UISnapshotNativeRuntimeMessages`. |
| Claude shared child conversation viewing | `MEKUGI_TEST_NATIVE_CLAUDE=1 make test TEST_PACKAGES=./internal/router TEST_RUN='^TestNativeRuntimeAgentsClaudePTY$' TEST_FLAGS='-count=1 -v -timeout=3m'` after `make test-claude`; installed-native local-provider PTY proves child text before settlement, shared keyboard/mouse roster filtering, Main isolation, live output dialog, native model-tool SendMessage context retention, `/to` target autocomplete and busy-child delivery, directed Activity, unknown-target draft retention, roster stop and exactly-once effects. No inference; native terminal foreground switching and fresh-process PTY viewing remain separate. Offline rendering: `TestUISnapshotNativeRuntimeParity`. |
| Claude native Bash capture | `MEKUGI_TEST_NATIVE_CLAUDE=1 make test TEST_PACKAGES=./internal/router TEST_RUN='^TestNativeRuntimeBashCaptureClaudeLive$' TEST_FLAGS='-count=1 -v -timeout=2m'` after `make build-claude`; one real prompt in an isolated workspace verifies foreground success, partial failure, terminal background completion and three saved effects, with normal native billing/configuration |
| Claude native shared-UI PTY and restart | `MEKUGI_TEST_NATIVE_CLAUDE=1 make test TEST_PACKAGES=./internal/router TEST_RUN='^TestNativeRuntimeClaudePTYLive$' TEST_FLAGS='-count=1 -v -timeout=6m'` after `make test-claude`; two real prompts exercise streamed native Write, shared Diff scrolling/resizing/reconciliation and fresh UI-process native resume with saved capture before new input. Optional absolute `MEKUGI_NATIVE_CLAUDE_PTY_EVIDENCE` retains rendered frames |
| Claude streamed Bash proposals and branch continuity | After `make test-claude`, run `MEKUGI_TEST_NATIVE_CLAUDE=1 make test TEST_PACKAGES=./internal/router TEST_RUN='^TestNativeRuntime(CommandPreview\|PreviewContinuity)ClaudePTY$' TEST_FLAGS='-count=1 -v -timeout=3m'`. Installed-native local-provider PTY proves growing proposals before complete input/effects, scroll/resize, exact native input and independent capture reconciliation, plus child attribution, segment output/Events clicks and saved dialogs across fresh parent/fork/resume without replay. Child input is complete, not a partial stream. No inference. Offline state and reviewed Main/child snapshots: `NativeRuntimeCommandPreview`, `NativeRuntimePreview`, `UISnapshotNativeRuntimeCommandPreview`. |
| Claude journal-only reset and slice continuation | `MEKUGI_TEST_NATIVE_CLAUDE=1 make test TEST_PACKAGES=./internal/router TEST_RUN='^TestRuntimeJournalResetClaudeLive$' TEST_FLAGS='-count=1 -v -timeout=5m'` after `make test-claude`; four real prompts exercise a completed slice, fresh native query with bounded journal context, inherited MCP own-change reads, fresh bridge resume and another reset after resume without old conversation context or tool replay |
| Claude companion utilities, journals and classic recovery | `MEKUGI_TEST_NATIVE_CLAUDE=1 make test TEST_PACKAGES=./internal/router TEST_RUN='^TestRuntimeCompanionClaudeLive$' TEST_FLAGS='-count=1 -v -timeout=4m'` after `make test-claude`; real native Write, Bash utility review/revert, MCP batch/read and manual compact with additive recovery, normal native billing/configuration |
| Claude mandatory guidance delivery | `MEKUGI_TEST_NATIVE_CLAUDE=1 make test TEST_PACKAGES=./internal/router TEST_RUN='^TestRuntimeGuidanceClaudeNativeDelivery$' TEST_FLAGS='-count=1 -v -timeout=2m'` after `make test-claude`; installed native Claude with isolated configuration and a scripted local provider proves full generated guidance and MCP descriptors in fresh/resumed work requests, plus native named-section, engine-attachment and tool-description mod replacements; no model inference |
| Claude native child and compact guidance | `MEKUGI_TEST_NATIVE_CLAUDE=1 make test TEST_PACKAGES=./internal/router TEST_RUN='^TestRuntimeGuidanceClaudeNativeChildAndCompact$' TEST_FLAGS='-count=1 -v -timeout=2m'` after `make test-claude`; scripted native child and classic compact requests prove the complete workflow and authenticated frontend contracts, native summary preservation and exactly-once child execution. Mod replacements cover attachments and tool descriptions in both lanes; named preset sections cover post-compact Main while the custom child retains its own system instructions. No inference. |
| Claude confirmed current-context skills | `make test TEST_PACKAGES=./internal/router TEST_RUN='^TestUISnapshotNativeRuntime(Confirmed\|Saved)Skills$' TEST_FLAGS=-count=1` exercises the real JSON-lines decoder and shared renderer: canonical Skill success, successful Read, pending/failure exclusion, late child identity, trimmed feed, shared name dialogs, complete/unknown saved counts and Main compact/reset isolation. After `make test-claude`, run `MEKUGI_TEST_NATIVE_CLAUDE=1 make test TEST_PACKAGES=./internal/router TEST_RUN='^TestNativeRuntimeSavedSkillsClaudeNative$' TEST_FLAGS='-count=1 -v -timeout=2m'` for installed-native local-provider Skill/Read, fresh resume, native fork child selection and Main compact restoration. No inference. Child live context-boundary delivery remains separate. |
| Claude native Bash input tuples | `MEKUGI_TEST_NATIVE_CLAUDE=1 make test TEST_PACKAGES=./internal/router TEST_RUN='^TestNativeBashTupleClaudeScripted$' TEST_FLAGS='-count=1 -v -timeout=2m'` after `make test-claude`; twenty complete hook pairs across default/acceptEdits, coercion, normalization and background execution, with exactly-once effects and unchanged settings; no inference |
| Claude native command output and clicks | `MEKUGI_TEST_NATIVE_CLAUDE=1 make test TEST_PACKAGES=./internal/router TEST_RUN='^TestNativeRuntimeOutputClaudePTY$' TEST_FLAGS='-count=1 -v -timeout=3m'` after `make test-claude`; file-gated foreground/background commands prove incremental tails, complete Unicode background output, shared live dialogs and rendered Main/Activity Events/output clicks. Fresh client/UI resume with the native spool moved proves saved output without tool replay; no inference. Offline chunks, display-bound references and restart: `NativeRuntimeFullOutput`; shared continuation snapshot: `UISnapshotActivityOutputDialog/retained_line_bounds`. |
| Claude native command segments | `MEKUGI_TEST_NATIVE_CLAUDE=1 make test TEST_PACKAGES=./internal/router TEST_RUN='^TestRuntimeCommandSegmentsClaudeNative$' TEST_FLAGS='-count=1 -v -timeout=7m'` after `make test-claude`; installed native foreground/background success/failure and identical-command fallback verify shared segment output/exits/skips, exact effects, terminal gating and chained observer startup. Uses the shared UI event consumer, not PTY rendering; no inference. Offline differential and rendered coverage: `TestClaudeExecTrack`, `TestClaudeRuntimeSegments`, `TestUISnapshotNativeRuntimeCommandSegments`. |
| Claude segmented terminal interaction | `MEKUGI_TEST_NATIVE_CLAUDE=1 make test TEST_PACKAGES=./internal/router TEST_RUN='^TestNativeRuntimeSegmentsClaudePTY$' TEST_FLAGS='-count=1 -v -timeout=3m'` after `make test-claude`; gated installed-native commands prove live per-segment disclosure, isolated incremental Main/Activity dialogs, final exits/skips, Events clicks, foreground Ctrl-C and background dialog x stop with native terminal receipts, process death and exactly-once effects. Local scripted provider, no inference. |
| Claude saved command segments | `MEKUGI_TEST_NATIVE_CLAUDE=1 make test TEST_PACKAGES=./internal/router TEST_RUN='^TestNativeRuntimeSavedSegmentsClaudeNative$' TEST_FLAGS='-count=1 -v -timeout=3m'` after `make test-claude`; installed native root/child Bash, parent restart, query-created fork and fresh fork resume prove exact saved output/exits/skips, inherited child selection and exactly-once effects with a local scripted provider. Offline restart/isolation, storage errors and rendered cases: `NativeRuntimeSavedSegments`, `NativeForkHistory`, `UISnapshotNativeRuntimeSavedCommandSegments`. No inference or segmented PTY coverage. |
| Claude ordinary journal adoption | `MEKUGI_TEST_NATIVE_CLAUDE=1 make test TEST_PACKAGES=./internal/router TEST_RUN='^TestRuntimeJournalAdoptionClaudeLive$' TEST_FLAGS='-count=1 -v -timeout=4m'` after `make test-claude`; one ordinary Sonnet implementation prompt, without journal instructions from the user, must publish retained completed tasks in the shared Journal and pass an independent behavior grader; normal native billing/configuration |
| Claude shared Journal PTY and restart | `MEKUGI_TEST_NATIVE_CLAUDE=1 make test TEST_PACKAGES=./internal/router TEST_RUN='^TestNativeRuntimeJournalClaudePTYLive$' TEST_FLAGS='-count=1 -v -timeout=6m'` after `make test-claude`; one real MCP batch, terminal permission, shared Journal navigation/scroll/resize and fresh UI-process resume before new input. Optional absolute `MEKUGI_NATIVE_CLAUDE_JOURNAL_PTY_EVIDENCE` retains rendered frames |
| Configured frontend host acceptance | `-tags journal_e2e ./internal/router -run '^TestConfiguredToolFrontendNativeCodexE2E$'` (installed Codex, local mock provider) |
| Native mrun continuation acceptance | `-tags journal_e2e ./internal/router -run '^TestMRunNativeCodexYieldAndWriteStdinE2E$'` (installed Codex, local mock provider) |
| Native journal child-result acceptance | `-tags journal_e2e ./internal/router -run '^TestJournalNativeCodexSpawnE2E$'` (installed Codex, local mock provider) |
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

Journal contention checks compare authenticated HTTP latency with a synthetic
20 ms delivery hold in the same or an unrelated workspace:

```sh
make test TEST_PACKAGES=./internal/router TEST_RUN='^$' TEST_FLAGS='-bench=BenchmarkJournalConcurrentWorkspaceHTTP -benchtime=10x -count=1'
```

These are controlled contention measurements, not live-session latency claims.

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

## Native Claude VCS guard

Offline metadata, shared approval and snapshot checks use `NativeRuntimeVCSGuard`.
After `make test-claude`, run the installed-native local-provider acceptance:

```sh
MEKUGI_TEST_NATIVE_CLAUDE=1 make test TEST_PACKAGES=./internal/router TEST_RUN='^TestNativeRuntimeVCSGuard(Startup)?ClaudeNative$' TEST_FLAGS='-count=1 -v -timeout=2m'
```

It covers original native permission rules and arguments, reached/skipped writes,
command-local denial, wrappers, exact-session grants and once-only effects without
inference. Startup admission also proves project/legacy environment override,
missing startup resources/helper, shell-prefix and foreground/background competing-hook rejection before ready,
with no model/tool calls and preserved caller settings.
`ClaudeExecTrackGuard` covers real Bash helper/report/replacement delivery failure,
guard-only fallback and identical concurrent inputs without guessed row identity.

Guard handoff acceptance uses the same installed-native local provider:

```sh
MEKUGI_TEST_NATIVE_CLAUDE=1 make test TEST_PACKAGES=./internal/router TEST_RUN='^TestNativeRuntimeVCSGuard(Handoff|Children)ClaudeNative$' TEST_FLAGS='-count=1 -v -timeout=3m'
```

The handoff fixture checks A → B → A, clear and fresh A-launch B-resume against
exact argv/cwd/executable grants for the UI lifetime. The child fixture checks
independent reached requests, native cancellation, sibling continuity and
same-session shell shutdown/resume. Both check effects without inference.

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
