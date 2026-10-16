# Mekugi owner map


Paths are relative to the repository root. Start with the matching production entrypoint;
use its focused tests when choosing validation. These are navigation pointers, not a second
behavioral contract. UI presentation packages and router session integration are separate owners.

| Behavior | Authoritative area |
| --- | --- |
| Workspace enumeration and import-boundary validation | `apps/mekugi/cmd/workspace`; membership in `go.work` |
| Application composition and initial executable bindings | `apps/mekugi/internal/composition`, `contracts`, `backends/codex/binding`; scripted harness in `testsupport/moduletest` |
| Captured diff model, composition, merge and file status | `libraries/diffmodel` owns composition, grouping and alignment; `modules/changehistory/presentation` owns selected retained review; `libraries/diffrender` owns shared terminal geometry and syntax; `modules/changehistory/terminalview` paints saved changes |
| Shared source capability and shell-header semantics | `libraries/sourceanalysis/sourcekind`, `libraries/commandanalysis/shellsyntax` |
| Shared source parsing, literal decoding, and nonblocking source-file opens | `libraries/sourceanalysis/sourceparse`; execution classification uses `libraries/commandanalysis/execscope`, while preview evaluation remains in `apps/mekugi/internal/router` |
| Activity presentation, diff navigation/previews, and terminal primitives | `modules/activity/terminalview`, `modules/changehistory/terminalview`, `modules/livepreview/terminalview`, `libraries/terminalui`; native session integration remains in `apps/mekugi/internal/router` |
| Renderer-neutral activity content, grouping and retained output | `modules/activity/feed` owns parsing/retention; `modules/activity/presentation` owns attached feed/roster records, revisions, skill-context derivation and flat group membership; `apps/mekugi/internal/router/activity_output.go` scopes output snapshots and private dialog origins; `modules/activity/terminalview/disclosure.go` owns renderer annotations and `tail.go` owns reveal pacing |
| Native screen composition, transcript switching, and pane layout | `apps/mekugi/internal/router/native_shell.go`; preview fixtures in `apps/mekugi/internal/router/app_server_preview_test.go` |
| Applied settings, recovery evidence and renderer preferences | `modules/sessionpreferences/preferences`; terminal schema and host control adaptation in `apps/mekugi/internal/router/app_server_panes.go` and `app_server_resume.go` |
| Interactive request correlation and manual-compaction lifecycle | `apps/mekugi/internal/sessioncontrol`; router retains composer payloads and remaining host-operation integration |
| Remaining session/composer lifecycle and host-event reconciliation | `apps/mekugi/internal/router/app_server_ui.go`; input paths in `apps/mekugi/internal/router/app_server_input.go`; composer error display in `apps/mekugi/internal/router/composer_notice.go` |
| Source-aware selection, scrolling, and logical copy | `apps/mekugi/internal/router/terminal_selection.go`, `apps/mekugi/internal/router/terminal_source_selection_test.go`, `apps/mekugi/internal/router/terminal_selection_scroll_test.go` |
| Native approval dialog, Codex approval responses, and approval-mode thread policy | `apps/mekugi/internal/router/app_server_approvals.go`; question dock in `apps/mekugi/internal/router/app_server_questions.go` |
| Sudo command approval and secret password input | `modules/sudoapproval` owns option policy, password validation and per-UI grants; `apps/mekugi/cmd/mekugi-exec/sudo.go` bridges native sudo/askpass; `apps/mekugi/internal/router/sudo.go` owns secret-editor delivery. Shared wire/resource names live in `backends/codex/shellapproval`; contract in `modules/sudoapproval/SPEC.md`. |
| Remote VCS write guard: classification, command instrumentation, and approval hand-off | `modules/remotewriteapproval` owns classification and per-UI grants; `libraries/commandanalysis` owns shared facts; `libraries/shellinstrumentation` owns composed rewriting/display/startup. Application launch installs only composed resources; `apps/mekugi/cmd/mekugi-exec/guard*.go` evaluates expanded commands, `apps/mekugi/internal/router/vcs_guard.go` owns socket lifetime and `apps/mekugi/internal/router/app_server_approvals.go` presents decisions. Contract in `modules/remotewriteapproval/SPEC.md`. |
| Native shell hook protocol | `backends/codex/shellhook`; application launch registration, trusted helper dispatch, and UI admission consume it |
| Optional session naming | `modules/sessionnaming` owns per-thread policy and inference requests; `apps/mekugi/internal/router/session_naming.go` owns logical scope lifetime and result delivery; `apps/mekugi/internal/router/naming_inference.go` binds auth, transport and accounting; `apps/mekugi/internal/router/app_server_session_title.go` reconciles host name saves and paints the header. Submitted-input identity decoding lives in `backends/codex/binding`. |
| Running-process composer count and `/ps` dialog | `apps/mekugi/internal/router/running_processes.go`; paging and live output in the shared `apps/mekugi/internal/router/output_dialog.go`; live commands remain in the app-server session |
| Dialog integration, Markdown file links, and background fading | `apps/mekugi/internal/router/output_dialog.go`, `apps/mekugi/internal/router/markdown_file_dialog.go`, `modules/activity/terminalview/dialog.go`, `libraries/terminalui/fallback.go` |
| Shared Markdown, source-copy fragments and wrapping | `libraries/terminalui/markdown.go`, `copy.go`, `markdown_table.go`; feature cards remain in `modules/activity/terminalview/paint.go` |
| Shared display-text sanitization | `libraries/textsanitize`; retained activity content, diff rendering and Markdown use the same control/Unicode rules |
| Submitted file attachments | `apps/mekugi/internal/router/composer_file_attachment.go`; shared path formatting in `libraries/pathdisplay` |
| Provisional changes and source projection | `modules/livepreview/preview` owns attachment state; `modules/livepreview/projection` owns bounded evaluation; `apps/mekugi/internal/router/saved_review.go` owns always-on admission and retained receipts |
| Diff rendering and navigation | `modules/changehistory/presentation/review.go`, `modules/livepreview/terminalview/preview.go`; native composition in `apps/mekugi/internal/router/native_shell.go` |
| Selected-file editor handoff | `apps/mekugi/internal/router/native_file_editor.go`, `apps/mekugi/internal/router/terminal_ui.go`; shared editor runner and terminal restoration in `apps/mekugi/internal/router/app_server_composer_editor.go`, `apps/mekugi/internal/router/app_server_ui.go` |
| Shared Diff syntax and word highlighting | `libraries/diffrender/source.go`, `libraries/diffrender/word_diff.go`, `libraries/diffrender/theme.go`; source previews in `modules/livepreview/terminalview/preview.go` |
| Codex app-server stdio RPC and child-process lifecycle | `backends/codex/appserver` |
| Noninteractive app-server client and JSONL session evidence | `apps/mekugi/internal/headless`; admission and process dispatch in `apps/mekugi/internal/launch` |
| Codex execution-source and result recognition | `backends/codex/binding/execution_source.go` and `execution_evidence.go`; preview, continuation correlation, restored evidence and offline corpus inspection consume these facts without executing them |
| Host-selected rollout identity, bounded context usage and resume settings | `backends/codex/rollout`; resume precedence in `modules/sessionpreferences`; Activity projection stays in router consumers |
| Supplied rollout decoding, logical call identity and recorded command endpoints | `backends/codex/rollout/inspection.go`; query selection and retained-record joins stay in the inspection consumer, metric pairing in `modules/agentmetrics/evidence` |
| Router lifecycle, launch flags, modes, and HTTP endpoints | `apps/mekugi/internal/router/server.go`, `apps/mekugi/internal/router/flags.go` |
| Invocation-local Mekugi TOML configuration and service-tier selection | `apps/mekugi/internal/router/config.go`, `apps/mekugi/internal/router/service_tier.go`; OpenCode service configuration in `providers/opencode` |
| Third-party native-agent projection and Codex model metadata | `apps/mekugi/internal/router/subagent_bridge.go`, `apps/mekugi/internal/router/grok_catalog.go` |
| Third-party credentials, model metadata and inference transport | `providers/grok` authentication and OAuth retry; `providers/opencode` configuration, catalog, pinned snapshots, price metadata, endpoint selection and scoped affinity. Routing and accounting integration remain in the router |
| Provider request envelopes, tool catalogs, byte-preserving projection, stream normalization, JSON/SSE delivery and Responses lifecycle emission | `backends/codex/responses`; wire facts and exact JSON comparison in `libraries/modelprotocol`; provider policy and accounting integration remain in `apps/mekugi/internal/router/provider_boundary.go` and `apps/mekugi/internal/router/grok_client.go`; stock execution admission stays in `apps/mekugi/internal/router/execution_admission.go`; delivery accounting and diagnostics in `apps/mekugi/internal/router/response_delivery.go` |
| Connection-local Responses ancestry, steering admission and provider-prefix confirmation | `backends/codex/responses/session.go`, `session_provider.go`; socket lifetime and executor binding remain in `apps/mekugi/internal/router/server_websocket.go` |
| Bounded notices, delivery claims and attached feedback | `modules/notifications/notice` and `presentation`; request classification/private-failure integration in `apps/mekugi/internal/router/critical_errors.go`, terminal receipts in `composer_notice.go` |
| Root-visible child activity | `apps/mekugi/internal/router/subagent_activity.go`; ownership in `doc/architecture/activity.md` |
| Per-thread token/cost reports and final-answer stream ordering | `modules/usage/accounting`; answer ordering in `apps/mekugi/internal/router/final_answer_stream.go` |
| Codex-facing WebSocket socket lifecycle and control forwarding | `apps/mekugi/internal/router/server_websocket.go`; connection-local reconstruction, steering state and provider-prefix reconciliation in `backends/codex/responses` |
| OpenAI credentials, model exchange, HTTP inference and WebSocket connections | `providers/openai`; Codex header/framing policy in `backends/codex/responses/openai.go`; provider selection and capture binding in `apps/mekugi/internal/router/client.go` |
| Shared HTTP defaults, response admission, idle-read lifetime and inspected-body replay | `libraries/httptransport`; provider and diagnostic consumers share its timeout identity; final third-party rejection reads remain startup-cancelable |
| Provider-measured output throughput and response-byte receipt timing | `modules/usage/accounting`; shared measurement facts in `contracts/inference`, provider parsing in `backends/codex/responses`, receipt timing in `apps/mekugi/internal/router/stream_diagnostics.go`; capture snapshots in `modules/transportdiagnostics/capture` |
| Stock tool preservation and response observation | `apps/mekugi/internal/router/mekugi_proxy.go`, `apps/mekugi/internal/router/mekugi_response_transform.go`, `apps/mekugi/internal/router/native_apply_patch.go` |
| Shell/source execution-scope classification and bounded provider queries | `libraries/commandanalysis/execscope`; immutable stock command inputs and literal JavaScript extraction in `libraries/commandanalysis`; host observation and retained-change resolution remain in `apps/mekugi/internal/router/exec_observation.go` and `apps/mekugi/internal/router/exec_changes.go` |
| Model-visible duplicate output references | `modules/outputdeduplication`; composed input projections in `backends/codex/binding`; local estimates in `modules/outputdeduplication/internal/projection`, receipt aggregation in `modules/transportdiagnostics/capture/output_dedupe.go`; provider-prefix reuse in `backends/codex/responses` |
| Shell segment reports and measured command timing | `libraries/shellinstrumentation` (source), `modules/commandsegments/transport` (report protocol and invocation FIFO), `apps/mekugi/cmd/mekugi-exec`, `apps/mekugi/internal/router/exec_track.go`; isolated shell fixtures in `apps/mekugi/internal/router/exec_track_test.go`, sandbox acceptance in `apps/mekugi/internal/router/app_server_exec_track_codex_e2e_test.go` |
| Reserved MCP endpoint lifetime and stdio bridge | `libraries/mcptransport`; explicit `mcp-bridge` dispatch in `apps/mekugi/internal/launch/dispatch.go`; no normal-startup endpoint |
| Agent roster presentation | `modules/activity/presentation/model.go` owns semantic order/status; `apps/mekugi/internal/router/native_roster.go` paints it; native assignments in `apps/mekugi/internal/router/native_activity_assignments.go` |
| Saved Diff caller filtering and capture attribution | `apps/mekugi/internal/router/live_diff_data.go`, `apps/mekugi/internal/router/mekugi_store.go`; composition in `modules/changehistory/presentation/review.go`, selected counts in `modules/changehistory/presentation/counts.go` |
| Inactive instruction-file loading | `modules/contextreset`; supplied-history decoding in `codexbinding`, bounded source collection in `guidance`; no production caller |
| Projected frontend guidance | `modules/agentguidance/codexbinding` renders selected command descriptions and module-owned `guidance.md` contributions; `apps/mekugi/internal/frontends/registry.go` authenticates and publishes the invocation snapshot. |
| Inherited instruction cleanup and request-phase integration | `modules/agentguidance`; `apps/mekugi/internal/router/request_guidance.go`, `apps/mekugi/internal/router/instruction_cache_test.go`, `apps/mekugi/internal/router/stock_guidance_workflow_test.go`; recorded stock fixtures in `apps/mekugi/internal/router/testdata/instruction-cleanup/`, contract in `modules/agentguidance/SPEC.md` |
| AX runtime evidence and offline measurements | `modules/agentmetrics/evidence`; authenticated reader dispatch in `apps/mekugi/internal/frontends/worker.go` |
| Offline logical session inspection | `modules/sessioninspection/query` owns retained joins and findings; its `codexbinding` interprets host identities and execution evidence. `contracts/inspection` collects optional AX and provider usage; application CLI dispatch remains in `apps/mekugi/internal/router/session_inspect.go` and `session_corpus.go`. |
| Offline UI replay and profiling | `modules/sessionreplay/playback` owns reconstruction, scheduling and controls; `backends/codex/rollout/replay.go` decodes observations; `apps/mekugi/internal/router/session_ui_replay*.go` adapts terminal playback. Contract in `modules/sessionreplay/SPEC.md`, usage in `docs/contributing/development.md`. |
| Live session and offline replay performance profiling | `apps/mekugi/internal/router/profiling.go`, `apps/mekugi/internal/router/profiling_disabled.go`; diagnostic binary recipe in `Makefile` |
| Observed review capture and host outcomes | `apps/mekugi/internal/router/native_apply_patch.go`, `apps/mekugi/internal/router/exec_observation.go`, `apps/mekugi/internal/router/app_server_shell_edits.go`; pure review model in `libraries/diffmodel` |
| Retained change identities, review, frozen selections, apply/revert, and retirement | `modules/changehistory`; publication and live-view adapters in `apps/mekugi/internal/router/change_history.go` |
| Retained publication, namespaces, allocation, leases, and retention | `modules/sessionhistory/history`; composed record kinds in `apps/mekugi/internal/composition`, feature codecs in `apps/mekugi/internal/router/retained_record_kinds.go` |
| Cross-owner evidence publication, input adoption and fork initialization | `apps/mekugi/internal/recordtransactions`; router retains host-envelope conversion and request validation |
| Historical call-envelope decoding and snapshot validation | `libraries/legacycall`; router conversion in `apps/mekugi/internal/router/legacy_call.go` |
| Completed command-segment publication, compatibility, and adoption | `modules/commandsegments/reports`; host-fact and presentation integration in `apps/mekugi/internal/router/command_segments.go`; explicit readonly projection in `apps/mekugi/internal/router/storage_inspect.go` |
| Durable replay meaning, request-visible history, and retained output | `apps/mekugi/internal/router/mekugi_store.go`, `apps/mekugi/internal/router/mekugi_history.go`, `modules/outputrecovery` |
| Authenticated compiled frontend catalog, worker, and PATH | `apps/mekugi/internal/frontends` owns registry, dispatch, symlink wrappers and platform-specific executable pinning; `session.go` and `recovery.go` own worker leases and retained delivery |
| Symbol command arguments and activity display | `modules/symbolquery/query` owns parsing; `apps/mekugi/internal/router/subagent_tool_display_shell.go` consumes it |
| Shared frontend options, mcat selection grammar, and literal shell quoting | `libraries/commandargs`; readers, activity display, offline inspection and shell instrumentation share argument semantics; content reading and multi-target delivery remain with their callers |
| Bounded foreground command execution | `modules/boundedcommand`; authenticated dispatch in `apps/mekugi/internal/frontends/worker.go` |
| Structural inspection | `modules/sourceoutline`; shared declarations and source coordinates in `libraries/sourceanalysis/structure` |
| Raw source reading and shared output admission | `modules/fileread`; `libraries/outputbudget`; retained publication and paging belong to `modules/outputrecovery` |
| Router process signals, wrapped Codex lifecycle, and top-level exit | `apps/mekugi/internal/launch/dispatch.go`, `apps/mekugi/internal/launch/wrap.go`; executable entrypoint in `apps/mekugi/cmd/mekugi` |
| Normative interface requirements | `doc/spec/index.md` and the listed requirement file |
| Stable ownership contracts | `doc/architecture/index.md` and the listed contract file |

## Session evidence and UI performance

For questions about what an agent actually did, start with recorded session calls and results;
current source and contracts explain intended behavior, not historical execution. Codex rollouts
live under `$CODEX_HOME/sessions`, defaulting to `~/.codex/sessions`. Locate an exact session ID
with a filename glob using `rg --files --hidden --no-ignore` in that directory, then verify
`session_meta` identity and workspace. Do not count child sessions or inherited fork history as
independent user sessions. The logical inspection interface is in `modules/sessioninspection/SPEC.md` (logical queries), `modules/diagnostics/SPEC.md` (debug/failure diagnosis).

- `mekugi inspect-session --session /absolute/path/to/rollout.jsonl` reads call identities and
  outcomes without running recorded operations. Select private text only when needed.
- For debug bundles, use `mekugi inspect-session --debug-dir /absolute/path/to/debug --field
  diagnostic --limit 5 --text-bytes 512`; narrow further with `--request-id ID` when known.
  Paginate remaining evidence with `--offset`. Select JSON fields for a smaller view rather
  than piping minified JSON through a line limit such as `head`.
- For reproducible UI lag, the replay command accepts a literal session ID, not a rollout path:
  `bin/mekugi-pprof replay-session --session ID --headless --width 160 --height 48`.
  Build with `make mekugi-pprof`; use the printed profiling URL with `go tool pprof`.
  Keep inputs, speed and dimensions fixed for comparisons.
  Replay measures rendering, not terminal backpressure or live host/provider latency; use live
  profiling for those. The replay contract owns available selectors and limitations.

When investigating an older running installation, identify that process's binary and artifact
locations before applying the current storage layout. Use `CONTEXT-TESTS.md` for focused checks;
its owner table remains the validation authority.
For live CPU/heap faults, sample the identified process and resume the identified session.
Replay or a fresh session can miss retained process state and does not replace that evidence.
