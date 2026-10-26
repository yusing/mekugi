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
| Inference gateway | `inferencegateway`: Responses admission, provider selection, socket lifetime, catalog/auxiliary endpoints; `models.go`: authenticated catalog forwarding and Codex execution-mode projection; `service_tier.go`: applied tier shared by requests/display |
| Request execution | `requestexecution`: shared HTTP/WebSocket attempt, preparation, delivery, cancellation and failure; `{request_guidance,response_delivery,stream_diagnostics,naming_inference}.go`: integration seams |
| Execution observation | `executionobservation`: passive stock-tool observations, capture windows, completion and publication; `{exec_observation,exec_changes,exec_report_observation,shell_observation,native_apply_patch,final_answer_stream}.go`: focused seams |
| Session control | `sessioncontrol/client.go`: sole host connection/request ledger; `presets.go`: native configuration-backed preset reads/writes; package owns submission, interruption, compaction, settings, startup/resume, prompts, approvals, side threads and authenticated audience |
| Session presentation | `sessionpresentation`: scoped live/replay admission and detached publications; `{observation,turn,transcript_observation,input_workflow,control_response}.go`: turn/input reduction; `coordination.go`: shared coordination traffic; `activity_*.go`: source binding, history, roster, processes, details and output demand; `{menu,panel,resume}.go`: overlays |
| Session API | `sessionapi`: shared runtime admission, renderer-neutral session projection, control intents, discovery and bounded streams; `family.go`: ordinary-thread navigation and aggregate captures; `review.go`: shared saved-review projection; `sessionapi/CONTRACT.md`: shared request and event interface |
| Browser UI | `webui`: embedded assets-only host and Svelte browser binding; `webui/SPEC.md`: explicit machine selection, shared controls and replay |
| Server / Connector | Launch `server.go`, `server_start.go`: in-process execution composition; `sessionapi/control_socket.go`: versioned peer-admitted Unix WebSocket; `sessionapi/control_start.go`: serialized startup readiness; `connector`: token/origin admission and WebSocket forwarding; Launch `connector.go`: listener and token-rotation lifetime |
| Terminal session | `terminalsession`: actual terminal attachment, input precedence, geometry, frozen selection, detached views and frame delivery; `{attachment,interaction,main_keys,selection_source,shell,frame_render}.go`: entry points; contributions attach through `Views` |
| Session review | `sessionreview`: authorized retained membership, hydration and asynchronous immutable review projections; Launch `session_review.go` reconciles receipts, `native_shell.go` delivers panes |
| Record transactions | `recordtransactions`: cross-owner publication, retained-input adoption and fork initialization; Execution Observation retains host-envelope conversion/request validation |
| Inspection CLI | `inspectioncli/{session_inspect,session_corpus,storage_inspect}.go`: offline command dispatch; feature owners interpret evidence |

## Features and shared implementations

| Area | Owners and boundaries |
| --- | --- |
| Thread control | `modules/threadcontrol`: server-owned logical trees, admitted agent operations, prepared children and retained waits; Session API owns native runtime lifetime, Session Control owns each runtime’s host ledger |
| Activity | `modules/activity/module.go`: historical wait-snapshot retention; `feed`: content/output retention and `contracts/activitydetail` host; `coordination`: shared thread-control observation decoding; `presentation`: lifecycle, grouping, source/segment interpretation, roster, commit enrichment and detached publications; `terminalview`: layout, reveal/pacing, cursors, navigation and painting |
| Conversation | `modules/conversation/{composer,presentation,presets,prompt,completion}`: edits/attachments, drafts/queues/side values, durable reusable input slots, prompts and completion; `transcript`/`codexbinding`: message correlation/history/native mapping; `terminalview`: keys/painting. Session Control owns live replies/dispatch; Launch owns scans, skill writes and image lifetime |
| Composer predictions | `launch/composer_predictions.go`: asynchronous configuration and source/draft eligibility; `sessioncontrol/prediction.go`: host fork, turn and cleanup; `providers/openai/predictions.go`: authenticated remote configuration and moderation policy |
| Saved changes | `modules/changehistory`: identities, review, frozen selections, apply/revert and retirement; `capture`: bounded acquisition/checkpoints/scope merging/completion; `presentation`: immutable review/counts/details; `terminalview`: pane interaction/painting |
| Provisional changes | `modules/livepreview/{preview,codexbinding,projection}`: source admission/endpoints, arriving input/baselines/pacing, bounded evaluation; `terminalview/lifecycle.go`: reveal/settling. Execution Observation continues evidence comparison after source-card settlement |
| Diff primitives | `libraries/diffmodel`: composition/grouping/alignment; `libraries/diffrender`: geometry, syntax and word highlighting |
| Shared UI | `libraries/terminalui`: panes/dialogs, Markdown/tables, copy fragments and choices; `{images,image_frame}.go`: terminal graphics capabilities, encoding and lifetime; `libraries/textsanitize`: shared display sanitization; `libraries/pathdisplay`: workspace paths |
| Notices | `modules/notifications/{notice,presentation}`: bounded notices/claims/feedback; Request Execution classifies failures, Launch `{critical_errors,composer_notice}.go` binds audience/receipts |
| Settings/naming | `modules/sessionpreferences`: applied settings/resume precedence; Terminal Session `preferences.go`: layout restoration; `modules/sessionnaming`: naming policy/requests; Launch `{session_naming,app_server_session_title}.go`: lifetime/name saves |
| Approvals | `modules/remotewriteapproval`: VCS classification/grants; `modules/sudoapproval`: sudo policy/password validation; `apps/mekugi/cmd/mekugi-exec/{guard*,sudo}.go`: native helpers; Session Control `{shell_approvals,approval,question}.go`: pending decisions/grants/replies |
| Command segments | `modules/commandsegments/{segments,transport,reports}`: live correlation/subscriptions, FIFO, retained publication/adoption; `libraries/shellinstrumentation`: argv/source instrumentation; Launch `exec_track.go`, Execution Observation `exec_report_observation.go`: host integration |
| Session history | `modules/sessionhistory/history`: namespaces, leases, enrollment, retention and host-call cache; `{calls,call_requests,host_records}.go`: cache/pins/envelopes. Feature descriptors own record kinds; Record Transactions coordinates publication |
| Persistence | `libraries/persistence`: private publication/opened-file admission; `libraries/legacycall`: historical envelopes/snapshot validation; Execution Observation `legacy_call.go`: working-value conversion |
| Guidance | `modules/agentguidance`: inherited instruction cleanup, compaction selection, retained summary codec/authorized reads and `compact/capture.go` source acquisition; Execution Observation `compact_summary.go`: native reader scheduling and recovery; Codex Rollout `compaction.go`: exact native-boundary/reference joins for live, restored and replayed summary dialogs; `codexbinding`: selected frontend descriptions/module `guidance.md`; Frontends `registry.go`: authenticated snapshot. |
| Readers | `modules/{fileread,sourceoutline,symbolquery}`: raw reads, structural inspection and semantic queries; `libraries/{commandargs,outputbudget}`: grammar/admission; `libraries/sourceanalysis/structure`: shared declarations/coordinates |
| Command/output tools | `modules/boundedcommand`: bounded foreground execution; `modules/outputrecovery`: retained publication/paging; `modules/outputdeduplication`: duplicate-output projection/local estimates; `backends/codex/binding`: composed inputs |
| Source/command facts | `libraries/sourceanalysis/{sourcekind,sourceparse}`: capability/parsing/literal decoding/nonblocking opens; `libraries/commandanalysis/{shellsyntax,execscope}`: shell headers and execution scopes; callers retain authorization/effects |
| Metrics/diagnostics | `modules/usage/accounting`: provider tokens/cost/throughput; `contracts/inference`: measurement facts; `modules/transportdiagnostics/capture`: transport receipts/estimates; `modules/agentmetrics/evidence`: AX evidence; `modules/diagnostics`: debug/private failures |
| Inspection/replay | `modules/sessioninspection/{query,codexbinding}`: retained joins/findings/host evidence; `contracts/inspection`: optional AX/usage; `modules/sessionreplay/playback`: reconstruction/scheduling/controls; Launch `session_ui_replay*.go`: terminal adapter |

## Backend and providers

| Area | Owner |
| --- | --- |
| Codex RPC/processes | `backends/codex/appserver` |
| Native facts | `backends/codex/binding`: notifications/items, tool results, history identity, submitted text, execution source/results, catalogs and continuation evidence; `backends/codex/nativetrace`: bounded trace correlation |
| Rollouts | `backends/codex/rollout`: selected identity/context/resume evidence; `settings_file.go`: plain/compressed resume-settings acquisition; `{inspection,replay}.go`: logical calls/endpoints and replay observations |
| Composer recall | `backends/codex/messagehistory`: shared Codex input-history format, locking and retention; Launch `app_server_persistent_history.go`: asynchronous reads/writes; Conversation composer: recall and restored-entry reconciliation |
| Responses | `backends/codex/responses`: envelopes/catalogs, byte-preserving projection, streams/lifecycle and Codex framing; `compact_plaintext.go`: ordinary-turn compaction request adaptation; `{session,session_provider}.go`: connection ancestry/steering/prefix confirmation; `libraries/modelprotocol`: wire facts/exact JSON |
| Shell hooks | `backends/codex/shellhook`: registration/protocol; `backends/codex/shellapproval`: shared approval wire/resource names; Launch installs composed resources |
| Providers | `providers/openai`: credentials/model exchange/HTTP/WebSockets; `providers/grok`: authentication/OAuth retry; `providers/opencode`: configuration/catalog/pinned prices/endpoints/affinity |
| Transports | `libraries/httptransport`: defaults, response admission, idle-read lifetime and body replay; `libraries/mcptransport`: reserved endpoint/stdio bridge, explicitly dispatched by Launch, absent from normal startup |

## Integration seams

Launch adapts host effects and terminal delivery; feature/presentation owners retain semantics.
Cross-owner adapter entry points:

- Live/replay observations: `launch/{attached_presentation,app_server_events,session_ui_replay_attachment}.go`.
- Activity sources/painter/output: `launch/activity_terminal.go`; VCS facts: `activity_vcs.go`;
  enrichment resources: `activity_commit_reader.go`; auxiliary observations: `activity_supplements.go`.
- Transcript images: `launch/image_previews.go` acquires images admitted by
  `sessionpresentation/activity_images.go`; `modules/activity/terminalview/images.go` owns placement.
- Reusable input slots: `launch/composer_presets.go` binds Conversation presets to shared menus/input.
- Native input/frame delivery: `launch/{native_client_loop,native_client_input,native_client_render}.go`;
  shared pane painting: `launch/native_shell.go`.
- Conversation forks, rewind and side input: `sessioncontrol/{startup,side_workflow}.go`;
  Launch `{app_server_rewind,app_server_btw}.go` integrates pickers and thread-scoped input.
  `sessionreview/rewind.go` selects retained changes at the rewind boundary.
- Dialog/file admission: `launch/{activity_details,markdown_file_dialog}.go`;
  native editor/restoration: `launch/{native_client_editor,native_client_restoration}.go`.
- Guidance consumers: `executionobservation/instruction_cache_test.go`, Launch prewarm,
  `stock_guidance_workflow_test.go` and `testdata/instruction-cleanup/`.

## Boundary contracts

Load contracts for boundaries the change crosses. They own details and acceptance cases;
they do not expand the requested scope.

- **Host execution and logical coordination:** [Codex execution](backends/codex/SPEC.md#execution-authority),
  [thread coordination](modules/threadcontrol/SPEC.md),
  [frontends](apps/mekugi/internal/frontends/SPEC.md), [MCP](libraries/mcptransport/SPEC.md),
  [headless](apps/mekugi/internal/headless/SPEC.md), and [launch](apps/mekugi/internal/launch/SPEC.md).
- **Change evidence:** [capture](docs/contracts/change-evidence.md),
  [changes](modules/changehistory/SPEC.md), and [publication](docs/contracts/record-publication.md).
- **Scope/isolation:** [authority and publication](docs/contracts/record-publication.md) and
  [activity identity](modules/activity/SPEC.md#observation-authority).
- **Forks, side threads, switching, and resume:** [replay](apps/mekugi/internal/frontends/SPEC.md),
  [changes](modules/changehistory/SPEC.md), and [guide](modules/agentguidance/SPEC.md).
- **Independent reads and apply/revert dependencies:** [changes](modules/changehistory/SPEC.md).
- **Presentation:** [terminal interaction](apps/mekugi/internal/terminalsession/SPEC.md),
  [session behavior](apps/mekugi/internal/sessionpresentation/SPEC.md),
  [activity display](modules/activity/CONTRACT.md), and [session control](apps/mekugi/internal/sessioncontrol/SPEC.md).
- **Frontend/guidance ownership:**
  [plugin boundary](apps/mekugi/internal/frontends/SPEC.md) and [guide](modules/agentguidance/SPEC.md).
- **Responsiveness:** [presentation boundary](apps/mekugi/internal/terminalsession/SPEC.md#boundary).
- **Progress, capture, diagnostics, and providers:** [activity](modules/activity/SPEC.md),
  [notices](modules/notifications/SPEC.md), [metrics](modules/transportdiagnostics/SPEC.md), and [providers](docs/contracts/provider-routing.md).

## Session evidence and profiling

Load the section matching the investigation:

- Recorded behavior or debug bundles: [Inspect a session](docs/contributing/development.md#inspect-a-session).
- Retained-session replay: [Replay a session](docs/contributing/development.md#replay-a-session).
- Live CPU/RAM faults or profiling: [Profile live sessions and replay](docs/contributing/development.md#profile-live-sessions-and-replay).

Contracts: `modules/{sessioninspection,diagnostics,sessionreplay}/SPEC.md`.
Profiling implementation: `launch/{profiling,profiling_disabled}.go`.
Controlled model comparisons live in [codex-setup-ab](https://github.com/yusing/codex-setup-ab);
select them only for a comparison task.

## Workspace and generation

`apps/mekugi/cmd/workspace` owns enumeration/import checks; `go.work` owns membership,
module-root descriptors own contributions, and `make check-generated` checks reference drift.
