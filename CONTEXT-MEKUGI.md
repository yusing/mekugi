# Owner map

Generated module dependencies and bindings: `docs/reference/modules.md`. Paths are
repository-relative; braces group alternatives. Application owners below are under
`apps/mekugi/internal/`.

## Application

| Owner | Entry points and responsibility |
| --- | --- |
| Composition | `composition/modules.go`: selected descriptors/bindings; `contracts`: shared interfaces; `testsupport/moduletest`: composed harness |
| Launch | `launch/{dispatch,wrap,invocation}.go`: process lifecycle and host effects; `{native_session,app_server_ui}.go`: invocation state and serialized session loop; `{native_process,native_process_supervisor}.go`: detached-server startup and diagnostics lifetime; executable: `apps/mekugi/cmd/mekugi` |
| Options/catalog | `launch/options`, `launch/modelcatalog`, `launch/catalog.go`: launch/resume configuration, selected provider metadata and bundled discovery |
| Frontends | `frontends/{registry,worker,session,recovery}.go`: authenticated catalog/PATH, dispatch, executable pinning, worker leases and retained delivery |
| Headless | `headless`: automation adapter and JSONL host evidence; Launch admits it through the shared server-owned runtime and task-owned family lifetime |
| Inference gateway | `inferencegateway`: Responses admission, provider selection, socket lifetime, catalog/auxiliary endpoints; `models.go`: authenticated catalog forwarding and Codex execution-mode projection; `service_tier.go`: applied tier shared by requests/display |
| Request execution | `requestexecution`: shared HTTP/WebSocket attempt, preparation, delivery, cancellation and failure; `{request_guidance,response_delivery,stream_diagnostics,naming_inference}.go`: integration seams; `instruction_retention.go`: per-thread model-visible instruction capture |
| Execution observation | `executionobservation`: stock-tool and synchronous native-boundary observations, capture windows, completion and publication; `{exec_observation,exec_changes,exec_report_observation,exec_intervals,shell_observation,native_apply_patch,native_boundary,final_answer_stream}.go`: focused seams; `completion_{capture,preparation}.go`: bounded retained evidence acquired before final-answer injection |
| Session control | `sessioncontrol/client.go`: sole host connection/request ledger; `presets.go`: native configuration-backed preset reads/writes; `inspection.go`: serialized live-session inspection; package owns submission, interruption, compaction, settings, startup/resume, prompts, approvals, side threads and authenticated audience |
| Session presentation | `sessionpresentation`: scoped live/replay admission and detached publications; `{observation,turn,transcript_observation,input_workflow,control_response}.go`: turn/input reduction; `coordination.go`: shared coordination traffic; `inspection.go`: current context and restored compaction metrics; `activity_*.go`: source binding, history, roster, processes, details and output demand; `{menu,panel,resume}.go`: overlays; `panel_metrics.go`: shared metrics and feature-use projection |
| Session API | `sessionapi`: shared runtime admission, renderer-neutral session projection, control intents, discovery and bounded streams; `native_{feed,prefix,queue}.go`: native entry deltas, complete-state capacity and queue composition; `native_frontend.go`: physical frontend associations, companion observation and leases; `family.go`: ordinary-thread navigation, shared questions, aggregate captures and bounded live previews; `review.go`: shared saved-review projection; `pages.go`: revision-bound retained pages and complete-content reads; `content_lines.go`: selected-source continuation; `sections.go`: bounded publication sections; `remote_details.go`: admitted remote-detail reads; `resources.go`: independent HTTP resource reads; `sessionapi/CONTRACT.md`: shared request and event interface |
| Browser UI | `webui`: embedded assets-only host and Svelte browser binding; `webui/app/src/lib/ui`: locally owned shadcn-svelte primitives and AI Elements compositions; `webui/SPEC.md`: explicit machine selection, shared controls and replay; `webui/DESIGN.md`: visual and interaction rules |
| Herdr panels | `herdr`: bounded adapter over the shared Session API server; native frontend associations and effects bind terminal selection and pane leases; `herdr/SPEC.md`: native roster/diff integration |
| Server / Connector | Launch `server.go`, `server_start.go`: in-process execution composition; `sessionapi/control_socket.go`: versioned peer-admitted Unix WebSocket; `sessionapi/control_start.go`: serialized startup readiness; `connector`: token/origin admission and WebSocket forwarding; `connector/resources.go`: authenticated HTTP resource forwarding; Launch `connector.go`: listener and token-rotation lifetime |
| Terminal session | `terminalsession`: actual terminal attachment, input precedence, geometry, frozen selection, detached views and frame delivery; `{attachment,interaction,main_keys,selection_source,shell,frame_render}.go`: entry points; contributions attach through `Views` |
| Session review | `sessionreview`: authorized retained membership, hydration and asynchronous immutable review projections; Launch `session_review.go` reconciles receipts, `native_shell.go` delivers panes |
| Record transactions | `recordtransactions`: cross-owner publication, retained-input adoption and fork initialization; Execution Observation retains host-envelope conversion/request validation |
| Inspection CLI | `inspectioncli/{session_inspect,session_corpus,storage_inspect}.go`: offline command dispatch; feature owners interpret evidence |

## Features and shared implementations

| Area | Owners and boundaries |
| --- | --- |
| Thread control | `modules/threadcontrol`: server-owned logical trees, admitted agent operations, prepared children/context preloads and retained waits; `{control,codexbinding}/peer.go`: attributed cross-session messages and foreign wait observations, admitted through `sessionapi/coordination.go`; `control/{waits,lifecycle}.go`: self-compaction checkpoints; `control/features.go`: launch-local accepted-use counts; `control/rewind.go`: retained saved-root selection; `codexbinding/input.go`: provider-only message identity projection; `codexbinding/inspect.go`: live/retained agent inspection and coordination-result metrics; Session API owns native runtime lifetime, Session Control owns each runtime’s host ledger |
| Activity | `modules/activity/module.go`: historical wait-snapshot retention; `feed`: content/output retention; `coordination`: shared thread-control observation decoding; `presentation`: lifecycle, grouping, source/segment interpretation, roster, commit enrichment and detached publications; `terminalview`: layout, reveal/pacing, cursors, navigation and painting |
| Conversation | `modules/conversation/{composer,presentation,presets,prompt,completion}`: edits/attachments, drafts/queues/side values, durable reusable input slots, prompts and completion; `transcript`/`codexbinding`: message correlation/history/native mapping; `terminalview`: keys/painting. Session Control owns live replies/dispatch; Launch owns scans, skill writes and image lifetime |
| Composer predictions | `launch/composer_predictions.go`: asynchronous configuration and source/draft eligibility; `sessioncontrol/prediction.go`: host fork, turn and cleanup; `providers/openai/predictions.go`: authenticated remote configuration and moderation policy |
| Saved changes | `modules/changehistory`: identities, review, frozen selections, apply/revert and retirement; `capture`: bounded acquisition/checkpoints/scope merging/completion; `presentation`: immutable review/counts/details; `terminalview`: pane interaction/painting |
| Provisional changes | `modules/livepreview/{preview,codexbinding,projection}`: source admission/endpoints, arriving input/baselines/pacing, bounded evaluation; `terminalview/lifecycle.go`: reveal/settling. Execution Observation continues evidence comparison after source-card settlement |
| Diff primitives | `libraries/diffmodel`: composition/grouping/alignment; `libraries/diffrender`: geometry, syntax and word highlighting |
| Shared UI | `libraries/terminalui`: panes/dialogs, Markdown/tables, copy fragments and choices; `{images,image_frame}.go`: terminal graphics capabilities, encoding and lifetime; `libraries/textsanitize`: shared display sanitization; `libraries/pathdisplay`: workspace paths |
| Notices | `modules/notifications/{notice,presentation}`: bounded notices/claims/feedback; Request Execution classifies failures, Launch `{critical_errors,composer_notice}.go` binds audience/receipts |
| Settings/naming | `modules/sessionpreferences`: applied settings/resume precedence; Launch `{native_session,app_server_settings}.go`: Main applied settings and target-specific dispatch; Terminal Session `preferences.go`: layout restoration; `modules/sessionnaming`: naming policy/requests; Launch `{session_naming,app_server_session_title}.go`: lifetime/name saves |
| Approvals | `modules/remotewriteapproval`: VCS classification; `modules/sudoapproval`: sudo policy and sudo/GPG secret validation; `apps/mekugi/cmd/mekugi-exec/{guard*,sudo,gpg,gpg_options}.go`: native helpers; Session Control `{shell_approvals,approval,question}.go`: pending decisions, attachment-owned grants and private secret replies |
| Command segments | `modules/commandsegments/{segments,transport,reports}`: live correlation/subscriptions, FIFO, retained publication/adoption; `libraries/shellinstrumentation`: argv/source instrumentation; Launch `exec_track.go`, Execution Observation `exec_report_observation.go`: host integration |
| Session history | `modules/sessionhistory/history`: namespaces, leases, enrollment, retention and host-call cache; `{calls,call_requests,host_records}.go`: cache/pins/envelopes. Feature descriptors own record kinds; Record Transactions coordinates publication |
| Persistence | `libraries/persistence`: private publication/opened-file admission; `libraries/legacycall`: historical envelopes/snapshot validation; Execution Observation `legacy_call.go`: working-value conversion |
| Guidance | `modules/agentguidance`: inherited instruction cleanup, `contextnotice` measured milestones, summary-free reset/preload selection, historical summary codec/authorized reads and `compact/capture.go` file/skill/captured-diff acquisition; Execution Observation `{compact_summary,context_preload}.go`: shared capture, native reader scheduling and recovery; `{executionobservation,requestexecution}/compaction_cost.go`: admitted self-reset preflight and prepared-input estimate, priced by Usage `accounting/input_cost.go`; Codex Rollout `compaction.go`: exact native-boundary/reference joins for live, restored and replayed summary dialogs; `codexbinding`: selected frontend descriptions/module `guidance.md`; Frontends `registry.go`: authenticated snapshot. |
| Environment facts | `modules/agentguidance/environment`: bounded snapshot and cache; `modules/agentguidance/codexbinding/environment.go`: base-instruction data projection; Execution Observation `request_preparation.go`: admitted workspace and native context-window refresh |
| Readers | `modules/{fileread,sourceoutline,symbolquery}`: raw reads, structural inspection and semantic queries; `libraries/{commandargs,outputbudget}`: grammar/admission; `libraries/sourceanalysis/structure`: shared declarations/coordinates |
| Command/output tools | `modules/boundedcommand`: bounded foreground execution; `modules/outputrecovery`: retained publication/paging; `modules/outputdeduplication`: duplicate-output projection/local estimates; `backends/codex/binding`: composed inputs |
| Source/command facts | `libraries/sourceanalysis/{sourcekind,sourceparse}`: capability/parsing/literal decoding/nonblocking opens; `libraries/commandanalysis/{shellsyntax,execscope}`: shell headers and execution scopes; `libraries/modelprotocol/attachments/references.go`: nested attachment-reference recognition shared by Conversation and Guidance; callers retain authorization/effects |
| Metrics/diagnostics | `modules/usage/accounting`: provider tokens/cost/throughput; `contracts/inference`: measurement facts; `modules/transportdiagnostics/capture`: transport receipts/estimates; `modules/agentmetrics/evidence`: AX evidence; `modules/diagnostics`: debug/private failures, `diagnostic/retained_instructions.go`: retained model-visible instructions and tools; `modules/diagnostics/diagnostic/process.go`: bounded detached-server stderr and exit evidence |
| Inspection/replay | `modules/sessioninspection/{query,codexbinding}`: retained joins/findings/host evidence; `contracts/inspection`: optional AX/usage; `modules/sessionreplay/playback`: reconstruction/scheduling/controls; Launch `session_ui_replay*.go`: terminal adapter |

## Backend and providers

| Area | Owner |
| --- | --- |
| Codex RPC/processes | `backends/codex/appserver` |
| Claude delegates | `backends/claude`: native model catalog, bundled Agent SDK worker and correlated process transport; Launch joins its lifetime to Thread Control |
| Native facts | `backends/codex/binding`: notifications/items, tool results, history identity, submitted text, execution source/results, catalogs and continuation evidence; `backends/codex/nativetrace`: bounded trace correlation |
| Rollouts | `backends/codex/rollout`: selected identity/context/resume evidence; `settings_file.go`: plain/compressed resume-settings acquisition; `{inspection,replay}.go`: logical calls/endpoints and replay observations; `discover.go`: thread-ID rollout discovery |
| Composer recall | `backends/codex/messagehistory`: shared Codex input-history format, locking and retention; Launch `app_server_persistent_history.go`: asynchronous reads/writes; Conversation composer: recall and restored-entry reconciliation |
| Responses | `backends/codex/responses`: envelopes/catalogs, byte-preserving projection, streams/lifecycle and Codex framing; `compact_plaintext.go`: native reload-reader program; `{session,session_provider}.go`: connection ancestry/steering/prefix confirmation; `libraries/modelprotocol`: wire facts/exact JSON |
| Shell hooks | `backends/codex/shellhook`: registration/protocol; `backends/codex/shellapproval`: shared approval wire/resource names; Launch installs composed resources |
| Providers | `providers/openai`: credentials/model exchange/HTTP/WebSockets; `providers/grok`: authentication/OAuth retry; `providers/opencode`: configuration/catalog/pinned prices/endpoints/affinity |
| Transports | `libraries/httptransport`: defaults, response admission, idle-read lifetime and body replay; `libraries/mcptransport`: invocation-local endpoints/stdio bridge for composed coordination contributions, started by Launch |

## Integration seams

Launch adapts host effects and terminal delivery; feature/presentation owners retain semantics.
Cross-owner adapter entry points:

- Live/replay observations: `launch/{attached_presentation,app_server_events,session_ui_replay_attachment}.go`;
  `attached_presentation.go` keeps execution hooks stable across presentation replacement.
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

- **Host execution and logical coordination:** `backends/codex/SPEC.md#execution-authority`,
  `modules/threadcontrol/SPEC.md`, `libraries/mcptransport/SPEC.md`,
  `apps/mekugi/internal/{frontends,headless,launch}/SPEC.md`.
- **Change evidence:** `docs/contracts/{change-evidence,record-publication}.md`,
  `modules/changehistory/SPEC.md`.
- **Scope/isolation:** `docs/contracts/record-publication.md`,
  `modules/activity/SPEC.md#observation-authority`.
- **Forks, side threads, switching, and resume:**
  `apps/mekugi/internal/frontends/SPEC.md#lifecycle-and-replay`,
  `modules/{changehistory,agentguidance}/SPEC.md`.
- **Independent reads and apply/revert dependencies:** `modules/changehistory/SPEC.md`.
- **Presentation:** `apps/mekugi/internal/{terminalsession,sessionpresentation,sessioncontrol}/SPEC.md`,
  `modules/activity/CONTRACT.md`.
- **Frontend/guidance ownership:** `apps/mekugi/internal/frontends/SPEC.md`,
  `modules/agentguidance/SPEC.md`.
- **Responsiveness:** `apps/mekugi/internal/terminalsession/SPEC.md#boundary`.
- **Progress, capture, diagnostics, and providers:**
  `modules/{activity,notifications,transportdiagnostics}/SPEC.md`, `docs/contracts/provider-routing.md`.

## Local integration source

Herdr-app mods live in `~/.config/herdr-app/mods` in the home configuration repository.
The Mekugi mod source is `mods/mekugi/entry.js`; shared bridge sources are under
`~/.config/herdr-app/lib`. Regenerate bundled `main.js` with that repository’s
`~/.config/herdr-app/build-mods.ts`, rather than editing the bundle.

## Session evidence and profiling

Contracts: `modules/{sessioninspection,diagnostics,sessionreplay}/SPEC.md`.
Profiling implementation: `launch/{profiling,profiling_disabled}.go`.
Controlled model comparisons live in https://github.com/yusing/codex-setup-ab; select them only
for a comparison task.

## Workspace and generation

`apps/mekugi/cmd/workspace` owns enumeration, import and documentation-navigation checks;
`go.work` owns membership, module-root descriptors own contributions, and `make check-generated`
checks reference drift.
