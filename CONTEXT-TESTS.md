# Test procedures

Load only the procedures selected by the operation; routine focused checks use the runner and
applicable coverage below.

| Operation | Procedure |
| --- | --- |
| Terminal appearance, snapshot fixtures, or baseline checks/updates | [Terminal snapshots](docs/contributing/testing-snapshots.md) |
| Opt-in browser-control fixture or browser wire-type generation | [Browser control](docs/contributing/testing-browser-control.md) |
| Frontend/symbol benchmarks or test CPU/block profiles | [Performance measurements](docs/contributing/testing-performance.md) |
| Compile/run installed-Codex fixtures with local providers | [Installed-Codex acceptance](CONTEXT-AUTOMATED-TESTS.md) |
| Compile/run real-provider fixtures | [Live Codex requests](docs/contributing/testing-live-codex.md) |
| Verify an introduced or changed cost invariant | [Cost checks](CONTEXT-PERFORMANCE.md#cost-checks) |

## Runner

Test recipes preserve Go's native package outcomes, failure diagnostics and exit
status. Apply command-output bounds at the invocation when needed.

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

### Suite budget

Target 30 seconds for a fresh offline suite with warm build caches, without making it a completion
gate. Measure wall time when a full suite is warranted; report slower validation and continue.
Report cold build/race/live checks separately. Do not repeat passing tests or warm caches just to
meet the target. Investigate timing only for requested optimization or an evidenced task-related
regression. Local passes/model prose do not replace host results or missing runtime coverage.

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

After API changes, compile affected tagged fixtures using the compile-only command in the
matching installed-host or live-provider procedure above. Installed-host cases include native frontends/mrun/mchanges,
guards and sandbox reporting; compaction uses `TestAppServerCompactEnterInterruptsNativeCodex`
and `TestAppServerQueuedCompactContinuesNativeCodex`; naming persistence/resume lives in
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

`make lint` requires `golangci-lint` and `deadcode` on PATH. It uses `.golangci.yml`, checks production
and test reachability separately (including the snapshot harness), and fails on tool errors/findings.
Linux amd64 CI installs versions pinned in the release workflow.

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
- **Logical lifecycle:** for host-turn handling or thread-control waits, cover how host completion,
  failure, interruption, manual compaction, injected input and enclosing-cell re-execution keep or
  settle pending waits and logical work, per
  [thread control](modules/threadcontrol/SPEC.md#logical-waits-and-ordered-delivery).
- **Instruction projection:** mixed authorization/tool text, fenced examples and idempotence through
  stock, marked, custom and multipart developer text.

Contracts: `docs/index.md`. Dated host evidence: `docs/codex-router-e2e.md`.

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
