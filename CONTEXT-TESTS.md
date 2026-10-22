# Test procedures

## Runner

Use `make test`; the root has no Go module. Default `TEST_PACKAGES=@workspace` expands
explicit `go.work` members. Focused runs also include `testsupport/uisnapshot`:

```sh
make test TEST_PACKAGES=./apps/mekugi/internal/launch TEST_RUN='^TestShellRunnerMRun'
make test TEST_PACKAGES='./backends/codex/appserver ./apps/mekugi/internal/launch' TEST_RUN=AppServer
```

- Unchanged successful tests may use Go's cache. `TEST_FLAGS=-count=1` requests fresh results;
  `TEST_FLAGS='-count=3 -shuffle=on'` checks repetition/isolation; `TEST_FLAGS=-race` checks concurrency.
- `TEST_PARALLEL=32` overlaps process/PTY waits; reduce for constrained machines. Tests changing
  process-wide environment/cwd stay serial. Time fresh full runs and report compilation separately.
- The recipe clears `BASH_ENV`, `MEKUGI_EXEC_TRACK` and snapshot-update mode. For raw tests in an
  active Mekugi shell, use `env -u BASH_ENV -u MEKUGI_EXEC_TRACK`: startup hooks can put live
  frontends ahead of fixture PATH and route reads to the wrong store; inherited tracking can
  suppress isolated command reports.
- Tests neither generate assets nor install binaries. Interpreter/query fixtures need Node.js
  and ripgrep; native frontends/catalog do not need JavaScript.
- Linux amd64 release CI runs the fresh offline suite; other platforms retain command and
  welcome/version checks. Installed-Codex checks are separate in `CONTEXT-AUTOMATED-TESTS.md`.
  A focused pass covers only selected tests.

## Select coverage

Start with the changed package/module from `CONTEXT-MEKUGI.md`, then its affected consumers.
Use explicit member package paths, adding `/...` for a module's subpackages. Use `@workspace`
for broad/cross-package contracts. The table identifies integration seams, not a requirement
to run every listed family for every change. Application paths below are under
`apps/mekugi/internal/`; “Launch” means its `launch` package. Braces group alternatives.
Feature contracts in `docs/index.md` own acceptance details.

| Changed boundary | Additional consumers and coverage |
| --- | --- |
| Workspace/composition | `apps/mekugi/cmd/workspace`, `contracts/module`, `backends/codex/binding`, `testsupport/moduletest`; `composition`, `sessionpresentation`, Launch and affected modules. Cover descriptor lifetimes, real frontend composition, selected publications/startup; `make check-generated` checks reference drift |
| Activity/messages/history | `modules/activity`, `modules/conversation/{transcript,codexbinding}`, `contracts/scope`, `backends/codex/binding`, `sessionpresentation`; Launch live/child/restored/paged/replay consumers. Cover admission, attachment retirement, discovery/retry/cancel, anchoring, roster, source/segment interpretation, retained output, VCS directory/hash isolation and rendered rows |
| Input/control/settings | `modules/conversation`, `modules/sessionpreferences`, `sessioncontrol`, `sessionpresentation`, `backends/codex/binding`; Launch submission/steer, queues, prompts/approvals, side answers, shell, compaction, interruption, settings, resume/clear and replay. Cover draft restoration, picker acquisition/cancellation, scoped menus, secret input, clipboard lifetime, progress/notice precedence and actual rendered docks |
| Terminal/dialogs/diff | `terminalsession`, `libraries/{terminalui,diffrender,textsanitize}`, affected Activity/Change History/Live Preview views and Launch. Cover input precedence, geometry, frozen selection, wrapping/search/copy, page identity, output after feed aging, editor handoff, styles and native/replay frames. `make preview-native-ui` plays synthetic events without model requests |
| Review/capture/publication | `modules/{changehistory,livepreview,sessionhistory,outputrecovery}`, `sessionreview`, `recordtransactions`, `executionobservation`, `backends/codex/binding`; Launch patch/shell/yield, failed/partial effects, sibling/overlap, report settlement, fork/resume and recovery. Cover immutable publication, optional contributions/native fallback, receipts/badges, frozen selection, retention/leases, blocked-storage PTY and capacity/replacement |
| Filesystem admission | `libraries/{persistence,legacycall}`, `modules/activity/waittargets`; historical-call/snapshot/commentary/wait-target consumers. Cover regular publication, symlink replacement, special files, size limits and affected platforms |
| Host execution/segments/guards | `backends/codex/{appserver,nativetrace,binding,shellhook,shellapproval}`, `libraries/{shellinstrumentation,commandanalysis}`, `modules/{commandsegments,remotewriteapproval,sudoapproval}`, `apps/mekugi/cmd/mekugi-exec`; `frontends`, `sessioncontrol`, `composition`, Launch. Select RPC/shutdown, trace correlation/pending/yield/timeout/replay, `ExecTrack`, `CommandSegments`, `VCSGuard`, `Sudo`, hook/config and composed-approval cases; cover subscription concurrency and exact host behavior |
| Requests/providers | `inferencegateway`, `requestexecution`, affected `providers`, `libraries/{httptransport,modelprotocol}`, `backends/codex/responses`; Launch HTTP/WebSocket, models/catalog, auxiliary endpoints, credentials, prices, errors, prewarm, continuation/steering, cancellation, service tier and accounting. Cover request/history/tool/guidance projection, native collaboration, failed terminals and transport capture |
| Tools/guidance | Affected reader/command/output/guidance modules, `libraries/{commandargs,outputbudget,sourceanalysis}`, `frontends`; Launch authenticated dispatch, worker arguments, recovery/storage/fork, Activity interpretation and AX consumers. Instruction projection also uses `composition` and prepared requests; see focused commands below |
| Diagnostics/accounting/replay | Affected `modules/{usage,transportdiagnostics,agentmetrics,diagnostics,sessioninspection,sessionreplay}`, `contracts/{inference,inspection}`, `backends/codex/rollout`, `inspectioncli`; Launch token/cost/throughput, naming, debug/failure/retention, corpus, logical-session and replay consumers, including optional contributors/transports |
| Launch/headless/naming | Changed `launch/options`, `launch/modelcatalog`, `headless` or `modules/sessionnaming`; Launch dispatch/build, reused configuration, resume models, picker/startup, headless delivery/control/shutdown and title/name-save consumers |
| Session API | `sessionapi`, `sessionpresentation` and `modules/activity/presentation`; cover detached projection, prompt redaction, request admission, stream recovery, socket peer checks and hosted-resource cleanup. Runtime consumers require their own integration checks |

After API changes, compile affected tagged host fixtures using the compile-only command in
`CONTEXT-AUTOMATED-TESTS.md`. Installed-host cases include native frontends/mrun/mchanges,
guards and sandbox reporting; compaction uses `TestAppServerCompactEnterInterruptsNativeCodex`
and `TestAppServerQueuedCompactContinuesNativeCodex`; naming persistence/resume lives in
`launch/app_server_session_title_codex_e2e_test.go`.

## Focused tools and measurements

Native-reader latency covers a fresh authenticated worker, 3,000-row read, three-document
architecture batch and eight-file Go inspection at the default shared budget:

```sh
make test TEST_PACKAGES=./apps/mekugi/internal/launch TEST_RUN='^$' TEST_FLAGS='-bench=BenchmarkNativeFrontends -benchtime=3x -count=1'
```

The inspection batch exercises incomplete-output recovery. These benchmarks clear thread identity
and exclude active-session lease/retention costs. `BenchmarkSelectRows` in
`libraries/outputbudget/tokenizer` isolates row admission from startup/source I/O.

Go symbol acceptance needs an existing `gopls` and installs nothing. Opt in to cross-package/test
references and source freshness; resolver/frontend benchmarks use normal caches and exclude
frontend registry creation/active-session leases:

```sh
env MEKUGI_TEST_REAL_GOPLS=1 make test TEST_PACKAGES=./modules/symbolquery/query TEST_RUN='^TestNativeSymbolRealGoplsReferences$'
make test TEST_PACKAGES='./modules/symbolquery/query ./apps/mekugi/internal/launch' TEST_RUN='^$' TEST_FLAGS='-bench=BenchmarkNativeSymbol -benchtime=5x -count=1'
```

Frontend descriptions and supplementary `guidance.md` belong to command modules;
`modules/agentguidance` renders the selected catalog and its Codex binding owns layout/projection.
After changing frontend guidance, check authenticated snapshots and prepared requests:

```sh
make test TEST_PACKAGES='./modules/agentguidance/... ./apps/mekugi/internal/frontends ./apps/mekugi/internal/launch ./apps/mekugi/internal/composition' TEST_RUN='Guidance|Instruction|ConflictRewrite|Prewarm|ComposedFrontendCatalog'
```

For router profiles, pass temporary `-cpuprofile`, `-blockprofile` and `-o` paths through
`TEST_FLAGS`. Inspect CPU and block profiles with `go tool pprof`; summed blocked time is not wall time.

## Static checks

`make lint` requires `golangci-lint` and `deadcode` on PATH. It uses `.golangci.yml`, checks production
and test reachability separately (including the snapshot harness), and fails on tool errors/findings.
Linux amd64 CI installs versions pinned in the release workflow.

## Terminal snapshots

Use actual-renderer `TestUISnapshot` tests with fixed time, theme, dimensions and other
nondeterministic inputs. Owners retain fixtures in `testdata/snapshots/`. The staged-tree snapshot
commit gate is documented in `docs/contributing/development.md#build-and-test`.

```sh
make test-ui-snapshots SNAPSHOT_PACKAGES=./apps/mekugi/internal/launch SNAPSHOT='^TestUISnapshotNativeSessionShortcuts$'
```

Checks are offline, include the snapshot harness, and need neither Codex nor model requests.
Omit `SNAPSHOT_PACKAGES` for the workspace or `SNAPSHOT` for all snapshot cases. The same selections
apply to `make update-ui-snapshots`.

- `testsupport/uisnapshot.Assert` strips ANSI only: spacing, blank lines, wrapping and borders
  remain exact. Text comparisons do not prove color/style correctness.
- `AssertTerminal` paints into a fixed-width VT emulator and retains canonical ANSI as quoted rows,
  covering colors, emphasis, links, padding and style restoration independently of equivalent escape
  sequences. Include a following plain row to detect style leaks. Theme-sensitive cases cover
  terminal/dark/light; code fills also cover a reported background.
- A missing/changed fixture fails and writes a sibling `.txt.new` candidate plus a unified diff;
  the baseline stays unchanged. Review the candidate, then move it over the fixture or run the
  selected update recipe. Omitting `SNAPSHOT` updates all matching cases.
- Updates require `MEKUGI_UPDATE_UI_SNAPSHOTS=1`; check recipes clear it. A pass or explicit update
  removes stale candidates. Rerun checks after accepting a baseline.
- Keep copy/state/bounds/interaction/PTY checks separate. Git attributes exempt only snapshot text
  from trailing-space/final-blank-row warnings; ordinary sources still use `git diff --check`.

## Boundary acceptance

Apply these routes when the requested change touches the boundary:

- **Continuity:** restart durable review/output state with only the requesting thread; cover
  affected fork/switch/resume paths. Live ancestry is not retained identity.
- **Launcher handoff:** cancellation/rendering before and after Codex takes the terminal,
  including redirected output and delayed startup.
- **Live diff:** independent layout, viewport/follow and lifecycle checks plus real PTY rendered
  frames. Show input before completion, continued following, independent diff scroll, resize and
  preview removal. Report missing terminal coverage.
- **Stock edits:** exact nested tool arguments/results, one host execution, pre-completion streaming,
  failed/partial outcomes, durable `mchanges` evidence and dependent reads after persistence.
- **Execution:** batching/parallelism, interpreter display, PTY/yield/`write_stdin`, bounded output
  and `mread` recovery.
- **Producer shapes:** Chat/Responses and persisted rollout events at the consuming boundary.
  Recheck dated host observations after Codex upgrades.
- **Instruction projection:** mixed authorization/tool text, fenced examples and idempotence through
  stock, marked, custom and multipart developer text.

Contracts: `docs/index.md`. Dated host evidence: `doc/codex-router-e2e.md`.

## Fixture isolation and cost

Reuse immutable registries while isolating mutable thread/workspace/process state; startup/shutdown
checks need their own owners. Disposable Git fixtures isolate global/system configuration to avoid
personal signing/hooks, while retaining local settings for filter/worktree boundaries.

Resolve fixture shells with `execTrackShellExecutable` in `launch/exec_track_test.go`:
`exec.Command` resolves names before `Cmd.Env`, so environment isolation alone can select the live
approval guard. Keep fixture tools first for nested shells.

Use controlled time for in-process lifetimes, retention retries and filesystem locks. Keep subtests
and parallel scheduling outside `synctest` bubbles; create/clean up workers inside. Preserve real
process-cleanup coverage and prove boundary coverage before shrinking fixtures.
