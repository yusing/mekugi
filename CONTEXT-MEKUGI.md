# Owner map

Use this map to locate an owner, `CONTEXT-TESTS.md` to select checks, and `docs/index.md`
for specifications. Detailed module dependencies and bindings are generated in
`docs/reference/modules.md`. Paths are repository-relative; braces group alternatives.
Application owners below are under `apps/mekugi/internal/`.

## Application

| Owner | Entry points and responsibility |
| --- | --- |
| Composition | `composition/modules.go`: selected descriptors/bindings; `contracts`: shared interfaces; `testsupport/moduletest`: composed harness |
| Launch | `launch/{dispatch,wrap,invocation}.go`: process lifecycle, invocation owners and host effects; executable: `apps/mekugi/cmd/mekugi` |
| Options/catalog | `launch/options`, `launch/modelcatalog`, `launch/catalog.go`: launch/resume configuration, selected provider metadata and bundled discovery |
| Frontends | `frontends/{registry,worker,session,recovery}.go`: authenticated catalog/PATH, dispatch, executable pinning, worker leases and retained delivery |
| Headless | `headless`: noninteractive app-server client and JSONL evidence; Launch admits/dispatches it |
| Inference gateway | `inferencegateway`: Responses admission, provider selection, socket lifetime, catalog/auxiliary endpoints; `models.go`: invocation-scoped catalog cache and Codex execution-mode projection; `service_tier.go`: applied tier shared by requests/display |
| Request execution | `requestexecution`: shared HTTP/WebSocket attempt, preparation, delivery, cancellation and failure; `{request_guidance,response_delivery,stream_diagnostics,naming_inference}.go`: integration seams |
| Execution observation | `executionobservation`: passive stock-tool observations, capture windows, completion and publication; `{exec_observation,exec_changes,exec_report_observation,shell_observation,native_apply_patch,final_answer_stream}.go`: focused seams |
| Session control | `sessioncontrol/client.go`: sole host connection/request ledger; package owns submission, interruption, compaction, settings, startup/resume, prompts, approvals, side threads and authenticated audience |
| Session presentation | `sessionpresentation`: scoped live/replay admission and detached publications; `{observation,turn,transcript_observation,input_workflow,control_response}.go`: turn/input reduction; `activity_*.go`: source binding, history, roster, processes, details and output demand; `{menu,panel,resume}.go`: overlays |
| Terminal session | `terminalsession`: actual terminal attachment, input precedence, geometry, frozen selection, detached views and frame delivery; `{attachment,interaction,main_keys,selection_source,shell,frame_render}.go`: entry points; contributions attach through `Views` |
| Session review | `sessionreview`: authorized retained membership, hydration and asynchronous immutable review projections; Launch `session_review.go` reconciles receipts, `native_shell.go` delivers panes |
| Record transactions | `recordtransactions`: cross-owner publication, retained-input adoption and fork initialization; Execution Observation retains host-envelope conversion/request validation |
| Inspection CLI | `inspectioncli/{session_inspect,session_corpus,storage_inspect}.go`: offline command dispatch; feature owners interpret evidence |

## Features and shared implementations

| Area | Owners and boundaries |
| --- | --- |
| Activity | `modules/activity/feed`: content/output retention and `contracts/activitydetail` host; `presentation`: lifecycle, grouping, source/segment interpretation, roster/waits, commit enrichment and detached publications; `terminalview`: layout, reveal/pacing, cursors, navigation and painting; `waittargets`: retained snapshots |
| Conversation | `modules/conversation/{composer,presentation,prompt,completion}`: edits/attachments, drafts/queues/side values, prompts and completion; `transcript`/`codexbinding`: message correlation/history/native mapping; `terminalview`: keys/painting. Session Control owns live replies/dispatch; Launch owns scans, skill writes and image lifetime |
| Composer predictions | `launch/composer_predictions.go`: asynchronous configuration and source/draft eligibility; `sessioncontrol/prediction.go`: host fork, turn and cleanup; `providers/openai/predictions.go`: authenticated remote configuration and moderation policy |
| Saved changes | `modules/changehistory`: identities, review, frozen selections, apply/revert and retirement; `capture`: bounded acquisition/checkpoints/scope merging/completion; `presentation`: immutable review/counts/details; `terminalview`: pane interaction/painting |
| Provisional changes | `modules/livepreview/{preview,codexbinding,projection}`: source admission/endpoints, arriving input/baselines/pacing, bounded evaluation; `terminalview/lifecycle.go`: reveal/settling. Execution Observation continues evidence comparison after source-card settlement |
| Diff primitives | `libraries/diffmodel`: composition/grouping/alignment; `libraries/diffrender`: geometry, syntax and word highlighting |
| Shared UI | `libraries/terminalui`: panes/dialogs, Markdown/tables, copy fragments and choices; `libraries/textsanitize`: shared display sanitization; `libraries/pathdisplay`: workspace paths |
| Notices | `modules/notifications/{notice,presentation}`: bounded notices/claims/feedback; Request Execution classifies failures, Launch `{critical_errors,composer_notice}.go` binds audience/receipts |
| Settings/naming | `modules/sessionpreferences`: applied settings/resume precedence; Terminal Session `preferences.go`: layout restoration; `modules/sessionnaming`: naming policy/requests; Launch `{session_naming,app_server_session_title}.go`: lifetime/name saves |
| Approvals | `modules/remotewriteapproval`: VCS classification/grants; `modules/sudoapproval`: sudo policy/password validation; `apps/mekugi/cmd/mekugi-exec/{guard*,sudo}.go`: native helpers; Session Control `{shell_approvals,approval,question}.go`: pending decisions/grants/replies |
| Command segments | `modules/commandsegments/{segments,transport,reports}`: live correlation/subscriptions, FIFO, retained publication/adoption; `libraries/shellinstrumentation`: argv/source instrumentation; Launch `exec_track.go`, Execution Observation `exec_report_observation.go`: host integration |
| Session history | `modules/sessionhistory/history`: namespaces, leases, enrollment, retention and host-call cache; `{calls,call_requests,host_records}.go`: cache/pins/envelopes. Feature descriptors own record kinds; Record Transactions coordinates publication |
| Persistence | `libraries/persistence`: private publication/opened-file admission; `libraries/legacycall`: historical envelopes/snapshot validation; Execution Observation `legacy_call.go`: working-value conversion |
| Guidance | `modules/agentguidance`: inherited instruction cleanup; `codexbinding`: selected frontend descriptions/module `guidance.md`; Frontends `registry.go`: authenticated snapshot. `modules/contextreset` has supplied-history/collection code but no production caller |
| Readers | `modules/{fileread,sourceoutline,symbolquery}`: raw reads, structural inspection and semantic queries; `libraries/{commandargs,outputbudget}`: grammar/admission; `libraries/sourceanalysis/structure`: shared declarations/coordinates |
| Command/output tools | `modules/boundedcommand`: bounded foreground execution; `modules/outputrecovery`: retained publication/paging; `modules/outputdeduplication`: duplicate-output projection/local estimates; `backends/codex/binding`: composed inputs |
| Source/command facts | `libraries/sourceanalysis/{sourcekind,sourceparse}`: capability/parsing/literal decoding/nonblocking opens; `libraries/commandanalysis/{shellsyntax,execscope}`: shell headers and execution scopes; callers retain authorization/effects |
| Metrics/diagnostics | `modules/usage/accounting`: provider tokens/cost/throughput; `contracts/inference`: measurement facts; `modules/transportdiagnostics/capture`: transport receipts/estimates; `modules/agentmetrics/evidence`: AX evidence; `modules/diagnostics`: debug/private failures |
| Inspection/replay | `modules/sessioninspection/{query,codexbinding}`: retained joins/findings/host evidence; `contracts/inspection`: optional AX/usage; `modules/sessionreplay/playback`: reconstruction/scheduling/controls; Launch `session_ui_replay*.go`: terminal adapter |

## Backend and providers

| Area | Owner |
| --- | --- |
| Codex RPC/processes | `backends/codex/appserver` |
| Native facts | `backends/codex/binding`: notifications/items, tool results, history identity, submitted text, execution source/results, catalogs and continuation advice; `backends/codex/nativetrace`: bounded trace correlation |
| Rollouts | `backends/codex/rollout`: selected identity/context/resume evidence; `{inspection,replay}.go`: logical calls/endpoints and replay observations |
| Composer recall | `backends/codex/messagehistory`: shared Codex input-history format, locking and retention; Launch `app_server_persistent_history.go`: asynchronous reads/writes; Conversation composer: recall and restored-entry reconciliation |
| Responses | `backends/codex/responses`: envelopes/catalogs, byte-preserving projection, streams/lifecycle and Codex framing; `collaboration.go`: native-agent projection/restoration; `{session,session_provider}.go`: connection ancestry/steering/prefix confirmation; `libraries/modelprotocol`: wire facts/exact JSON |
| Shell hooks | `backends/codex/shellhook`: registration/protocol; `backends/codex/shellapproval`: shared approval wire/resource names; Launch installs composed resources |
| Providers | `providers/openai`: credentials/model exchange/HTTP/WebSockets; `providers/grok`: authentication/OAuth retry; `providers/opencode`: configuration/catalog/pinned prices/endpoints/affinity |
| Transports | `libraries/httptransport`: defaults, response admission, idle-read lifetime and body replay; `libraries/mcptransport`: reserved endpoint/stdio bridge, explicitly dispatched by Launch, absent from normal startup |

## Integration seams

Launch adapts host effects and terminal delivery; feature/presentation owners retain semantics.
Cross-owner adapter entry points:

- Live/replay observations: `launch/{attached_presentation,app_server_events,session_ui_replay_attachment}.go`.
- Activity sources/painter/output: `launch/activity_terminal.go`; VCS facts: `activity_vcs.go`;
  enrichment resources: `activity_commit_reader.go`; child supplements: `subagent_activity_pane.go`.
- Input/frame delivery: `launch/{app_server_input,terminal_input,terminal_selection,native_shell}.go`.
- Dialog/file admission: `launch/{activity_details,markdown_file_dialog}.go`;
  editor/terminal restoration: `{native_file_editor,app_server_composer_editor,app_server_ui}.go`.
- Guidance consumers: `executionobservation/instruction_cache_test.go`, Launch prewarm,
  `stock_guidance_workflow_test.go` and `testdata/instruction-cleanup/`.

## Session evidence and profiling

For historical behavior, start with recorded calls/results; current code explains intended behavior.
Codex rollouts live under `$CODEX_HOME/sessions` (default `~/.codex/sessions`). Locate an exact ID
with `rg --files --hidden --no-ignore` and a filename glob, then verify `session_meta` identity/workspace.
Child sessions and inherited fork history are not independent user sessions.

- `mekugi inspect-session --session /absolute/path/to/rollout.jsonl`: logical evidence without
  executing recorded operations. Select private text only when needed.
- Debug bundles: `mekugi inspect-session --debug-dir /absolute/path/to/debug --field diagnostic
  --limit 5 --text-bytes 512`; narrow by `--request-id`, paginate with `--offset`. Select JSON
  fields rather than line-limiting minified output.
- Replay: `bin/mekugi-pprof replay-session --session ID --headless --width 160 --height 48` takes
  a literal session ID, not a path. Build with `make mekugi-pprof`; use its profiling URL with
  `go tool pprof`. Fix inputs/speed/dimensions for comparisons. Replay measures rendering, not
  terminal backpressure or live host/provider latency.
- For an older running installation, identify its binary/storage locations first. Sample the
  actual process for live CPU/heap faults and resume that session; replay/fresh sessions can miss
  retained process state.

Contracts: `modules/{sessioninspection,diagnostics,sessionreplay}/SPEC.md`.
Profiling build/sampling/privacy: `docs/contributing/development.md#profile-live-sessions-and-replay`;
implementation: `launch/{profiling,profiling_disabled}.go`.

## Workspace and generation

`apps/mekugi/cmd/workspace` owns enumeration/import checks; `go.work` owns membership,
module-root descriptors own contributions, and `make check-generated` checks reference drift.
