# Test procedures

Load only the procedure matching the operation; routine focused checks need only the sections below.

| Operation | Procedure |
| --- | --- |
| Opt-in browser-control fixture or browser wire-type generation | docs/contributing/testing-browser-control.md |
| Frontend/symbol benchmarks or test CPU/block profiles | docs/contributing/testing-performance.md |
| Compile/run installed-Codex fixtures with local providers | CONTEXT-AUTOMATED-TESTS.md |
| Compile/run real-provider fixtures | docs/contributing/testing-live-codex.md |
| Verify an introduced or changed cost invariant | CONTEXT-PERFORMANCE.md#cost-checks |

## Runner

Use `make test`; the root has no Go module. `TEST_PACKAGES` defaults to `@workspace` (all `go.work`
members); `TEST_RUN` and `TEST_FLAGS` pass through to `go test`. With RTK, successful packages are
summarized; set `RTK=` for native output.

```sh
make test TEST_PACKAGES='./backends/codex/appserver ./apps/mekugi/internal/launch' TEST_RUN=AppServer
```

- `TEST_PARALLEL` (default 32) overlaps process/PTY waits. Tests changing process-wide
  environment/cwd stay serial.
- For raw `go test` in an active Mekugi shell, use `env -u BASH_ENV -u MEKUGI_EXEC_TRACK`: startup
  hooks can put live frontends ahead of fixture PATH and route reads to the wrong store; inherited
  tracking can suppress isolated command reports. The recipe already clears them.
- Tests neither generate assets nor install binaries. Interpreter/query fixtures need Node.js
  and ripgrep.
- CI runs the offline suite only on Linux amd64; other platforms run command and welcome/version
  checks.

A fresh offline suite with warm caches targets 30 seconds; it is not a completion gate. Report wall
time for full runs, and cold build/race/live checks separately. Do not rerun passing tests or warm
caches to meet it; investigate timing only for requested optimization or an evidenced task-related
regression.

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
| Filesystem admission | `libraries/{persistence,legacycall}`, historical-call and snapshot consumers. Cover regular publication, symlink replacement, special files, size limits and affected platforms |
| Host execution/segments/guards | `backends/codex/{appserver,nativetrace,binding,shellhook,shellapproval}`, `libraries/{shellinstrumentation,commandanalysis}`, `modules/{commandsegments,remotewriteapproval,sudoapproval}`, `apps/mekugi/cmd/mekugi-exec`; `frontends`, `sessioncontrol`, `composition`, Launch. Select RPC/shutdown, trace correlation/pending/yield/timeout/replay, `ExecTrack`, `CommandSegments`, `VCSGuard`, `Sudo`, hook/config and composed-approval cases; cover subscription concurrency and exact host behavior |
| Requests/providers | `inferencegateway`, `requestexecution`, affected `providers`, `libraries/{httptransport,modelprotocol}`, `backends/codex/responses`; Launch HTTP/WebSocket, models/catalog, auxiliary endpoints, credentials, prices, errors, prewarm, continuation/steering, cancellation, service tier and accounting. Cover request/history/tool/guidance projection, thread coordination, failed terminals and transport capture |
| Tools/guidance | Affected reader/command/output/guidance modules, `libraries/{commandargs,outputbudget,sourceanalysis}`, `frontends`; Launch authenticated dispatch, worker arguments, recovery/storage/fork, Activity interpretation and AX consumers. Instruction projection also uses `composition` and prepared requests; see focused commands below |
| Diagnostics/accounting/replay | Affected `modules/{usage,transportdiagnostics,agentmetrics,diagnostics,sessioninspection,sessionreplay}`, `contracts/{inference,inspection}`, `backends/codex/rollout`, `inspectioncli`; Launch token/cost/throughput, naming, debug/failure/retention, corpus, logical-session and replay consumers, including optional contributors/transports |
| Launch/headless/naming | Changed `launch/options`, `launch/modelcatalog`, `headless` or `modules/sessionnaming`; Launch dispatch/build, reused configuration, resume models, picker/startup, headless delivery/control/shutdown and title/name-save consumers |
| Session API | `sessionapi`, `sessionpresentation` and `modules/activity/presentation`; cover detached projection, prompt redaction, request admission, stream recovery, socket peer checks and hosted-resource cleanup. Runtime consumers require their own integration checks |

After API changes, compile affected tagged fixtures with the compile-only command in the matching
installed-host or live-provider procedure. Host compaction: `TestAppServerCompactEnterInterruptsNativeCodex`,
`TestAppServerQueuedCompactContinuesNativeCodex`; naming persistence/resume:
`launch/app_server_session_title_codex_e2e_test.go`.

## Focused checks

Go symbol acceptance needs an existing `gopls` and installs nothing. Opt in to cross-package/test
references and source freshness:

```sh
env MEKUGI_TEST_REAL_GOPLS=1 make test TEST_PACKAGES=./modules/symbolquery/query TEST_RUN='^TestNativeSymbolRealGoplsReferences$'
```

Frontend descriptions and supplementary `guidance.md` belong to command modules;
`modules/agentguidance` renders the selected catalog and its Codex binding owns layout/projection.
After changing frontend guidance, check authenticated snapshots and prepared requests:

```sh
make test TEST_PACKAGES='./modules/agentguidance/... ./apps/mekugi/internal/frontends ./apps/mekugi/internal/launch ./apps/mekugi/internal/composition' TEST_RUN='Guidance|Instruction|ConflictRewrite|Prewarm|ComposedFrontendCatalog'
```

## Static checks

`make docs-check` validates Markdown link targets and anchors; the pre-commit hook runs it when
documentation changes.

Run `make lint` (needs `golangci-lint` and `deadcode`) before committing Go changes: CI runs it
after the offline suite, so a failing test hides its findings.

`make generate` also refreshes the network-sourced OpenCode catalog snapshots; run it only for a
catalog refresh. For workspace references or the Session API schema, run
`go run ./apps/mekugi/cmd/workspace index-write` or `go run ./apps/mekugi/cmd/sessionapi-schema`.

## Boundary acceptance

Cover these when the change touches the boundary:

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
  Recheck dated host observations after Codex upgrades. Claude Agent SDK messages and control
  responses: build fixtures from captured SDK output, not hand-authored mock values, and run one
  live Claude delegate turn before reporting Claude delegate metrics or presentation as working;
  otherwise report that behavior as unverified.
- **Logical lifecycle:** for host-turn handling or thread-control waits, cover how host completion,
  failure, interruption, manual compaction, injected input and enclosing-cell re-execution keep or
  settle pending waits and logical work, per
  `modules/threadcontrol/SPEC.md#logical-waits-and-ordered-delivery`.
- **Instruction projection:** mixed authorization/tool text, fenced examples and idempotence through
  stock, marked, custom and multipart developer text.

Dated host evidence: `docs/codex-router-e2e.md`.

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
