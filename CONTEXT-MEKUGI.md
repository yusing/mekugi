# Mekugi owner map


Paths are relative to the repository root. Start with the matching production entrypoint;
use its focused tests when choosing validation. These are navigation pointers, not a second
behavioral contract. UI presentation packages and router session integration are separate owners.

| Behavior | Authoritative area |
| --- | --- |
| Workspace enumeration and import-boundary validation | `apps/mekugi/cmd/workspace`; membership in `go.work` |
| Application composition and initial executable bindings | `apps/mekugi/internal/composition`, `contracts`, `backends/codex/binding`; scripted harness in `testsupport/moduletest` |
| Captured diff model, composition, merge and file status | `libraries/diffmodel`; terminal painting remains in `internal/livediff` |
| Shared source capability and shell-header semantics | `libraries/sourceanalysis/sourcekind`, `libraries/commandanalysis/shellsyntax` |
| Shared source parsing, literal decoding, and nonblocking source-file opens | `libraries/sourceanalysis/sourceparse`; execution classification uses `internal/execscope`, while preview evaluation remains in `internal/router` |
| Activity presentation, diff navigation/previews, and terminal primitives | `internal/ui/activity`, `internal/ui/diffview`, `internal/ui/terminal`; native session integration remains in `internal/router` |
| Native screen composition, transcript switching, and pane layout | `internal/router/native_shell.go`; preview fixtures in `internal/router/app_server_preview_test.go` |
| Applied settings, recovery evidence and renderer preferences | `modules/sessionpreferences/preferences`; terminal schema and host control adaptation in `internal/router/app_server_panes.go` and `app_server_resume.go` |
| Session/composer controller and host-event reconciliation | `internal/router/app_server_ui.go`; input paths in `internal/router/app_server_input.go`; composer error display in `internal/router/composer_notice.go` |
| Source-aware selection, scrolling, and logical copy | `internal/router/terminal_selection.go`, `internal/router/terminal_source_selection_test.go`, `internal/router/terminal_selection_scroll_test.go` |
| Native approval dialog, Codex approval responses, and approval-mode thread policy | `internal/router/app_server_approvals.go`; question dock in `internal/router/app_server_questions.go` |
| Sudo command approval and secret password input | `modules/sudoapproval` owns option policy, password validation and per-UI grants; `apps/mekugi/cmd/mekugi-exec/sudo.go` bridges native sudo/askpass; `internal/router/sudo.go` owns secret-editor delivery. Shared wire/resource names live in `backends/codex/shellapproval`; contract in `modules/sudoapproval/SPEC.md`. |
| Remote VCS write guard: classification, command instrumentation, and approval hand-off | `modules/remotewriteapproval` owns classification and per-UI grants; `libraries/commandanalysis` owns shared facts; `libraries/shellinstrumentation` owns composed rewriting/display/startup. Application launch installs only composed resources; `apps/mekugi/cmd/mekugi-exec/guard*.go` evaluates expanded commands, `internal/router/vcs_guard.go` owns socket lifetime and `internal/router/app_server_approvals.go` presents decisions. Contract in `modules/remotewriteapproval/SPEC.md`. |
| Native shell hook protocol | `backends/codex/shellhook`; application launch registration, trusted helper dispatch, and UI admission consume it |
| Optional session naming | `modules/sessionnaming` owns per-thread policy and inference requests; `internal/router/session_naming.go` owns logical scope lifetime and result delivery; `internal/router/naming_inference.go` binds auth, transport and accounting; `internal/router/app_server_session_title.go` reconciles host name saves and paints the header. Submitted-input identity decoding lives in `backends/codex/binding`. |
| Running-process composer count and `/ps` dialog | `internal/router/running_processes.go`; paging and live output in the shared `internal/router/output_dialog.go`; live commands remain in the app-server session |
| Dialog integration, Markdown file links, and background fading | `internal/router/output_dialog.go`, `internal/router/markdown_file_dialog.go`, `internal/ui/activity/dialog.go`, `internal/ui/activity/faint.go` |
| Shared Markdown and activity painting | `internal/ui/activity/paint.go`; rendering coverage in `internal/ui/activity/*snapshot_test.go` |
| Submitted file attachments | `internal/router/composer_file_attachment.go`; shared path formatting in `libraries/pathdisplay` |
| Live-diff models and preview presentation | `internal/livediff/model.go`, `internal/ui/diffview/preview.go`; native composition in `internal/router/native_shell.go` |
| Selected-file editor handoff | `internal/router/native_file_editor.go`, `internal/router/terminal_ui.go`; shared editor runner and terminal restoration in `internal/router/app_server_composer_editor.go`, `internal/router/app_server_ui.go` |
| Shared Diff syntax and word highlighting | `internal/livediff/render.go`, `internal/livediff/word_diff.go`, `internal/livediff/theme.go`; source previews in `internal/ui/diffview/preview.go` |
| Codex app-server stdio RPC and child-process lifecycle | `backends/codex/appserver` |
| Codex execution-source and result recognition | `backends/codex/binding/execution_source.go` and `execution_evidence.go`; preview, continuation correlation, restored evidence and offline corpus inspection consume these facts without executing them |
| Host-selected rollout identity, bounded context usage and resume settings | `backends/codex/rollout`; resume precedence in `modules/sessionpreferences`; Activity projection stays in router consumers |
| Supplied rollout decoding, logical call identity and recorded command endpoints | `backends/codex/rollout/inspection.go`; query selection and retained-record joins stay in the inspection consumer, metric pairing in `modules/agentmetrics/evidence` |
| Router lifecycle, launch flags, modes, and HTTP endpoints | `internal/router/server.go`, `internal/router/flags.go` |
| Invocation-local Mekugi TOML configuration and service-tier selection | `internal/router/config.go`, `internal/router/service_tier.go`; OpenCode service configuration in `providers/opencode` |
| Third-party native-agent projection and Codex model metadata | `internal/router/subagent_bridge.go`, `internal/router/grok_catalog.go` |
| Third-party credentials, model metadata and inference transport | `providers/grok` authentication and OAuth retry; `providers/opencode` configuration, catalog, pinned snapshots, price metadata, endpoint selection and scoped affinity. Routing and accounting integration remain in the router |
| Provider request envelopes, tool catalogs, byte-preserving projection, stream normalization, JSON/SSE delivery and Responses lifecycle emission | `backends/codex/responses`; wire facts and exact JSON comparison in `libraries/modelprotocol`; provider policy and accounting integration remain in `internal/router/provider_boundary.go` and `internal/router/grok_client.go`; stock execution admission stays in `internal/router/execution_admission.go`; delivery accounting and diagnostics in `internal/router/response_delivery.go` |
| Connection-local Responses ancestry, steering admission and provider-prefix confirmation | `backends/codex/responses/session.go`, `session_provider.go`; socket lifetime and executor binding remain in `internal/router/server_websocket.go` |
| Automatic notices and root-visible child activity | `internal/router/critical_errors.go`, `internal/router/subagent_activity.go`; boundaries in `doc/architecture/notices.md` and `doc/architecture/activity.md` |
| Per-thread token/cost reports and final-answer stream ordering | `modules/usage/accounting`; answer ordering in `internal/router/final_answer_stream.go` |
| Codex-facing WebSocket socket lifecycle and control forwarding | `internal/router/server_websocket.go`; connection-local reconstruction, steering state and provider-prefix reconciliation in `backends/codex/responses` |
| OpenAI credentials, model exchange, HTTP inference and WebSocket connections | `providers/openai`; Codex header/framing policy in `backends/codex/responses/openai.go`; provider selection and capture binding in `internal/router/client.go` |
| Shared HTTP defaults, response admission, idle-read lifetime and inspected-body replay | `libraries/httptransport`; provider and diagnostic consumers share its timeout identity; final third-party rejection reads remain startup-cancelable |
| Provider-measured output throughput and response-byte receipt timing | `modules/usage/accounting`; shared measurement facts in `contracts/inference`, provider parsing in `backends/codex/responses`, receipt timing in `internal/router/stream_diagnostics.go`; capture snapshots in `modules/transportdiagnostics/capture` |
| Stock tool preservation and response observation | `internal/router/mekugi_proxy.go`, `internal/router/mekugi_response_transform.go`, `internal/router/native_apply_patch.go` |
| Shell/source execution-scope classification and bounded provider queries | `internal/execscope`; immutable stock command inputs and literal JavaScript extraction in `libraries/commandanalysis`; host observation and retained-change resolution remain in `internal/router/exec_observation.go` and `internal/router/exec_changes.go` |
| Model-visible duplicate output references | `modules/outputdeduplication`; composed input projections in `backends/codex/binding`; local estimates in `modules/outputdeduplication/internal/projection`, receipt aggregation in `modules/transportdiagnostics/capture/output_dedupe.go`; provider-prefix reuse in `backends/codex/responses` |
| Shell segment reports and measured command timing | `libraries/shellinstrumentation` (source), `modules/commandsegments/transport` (report protocol and invocation FIFO), `apps/mekugi/cmd/mekugi-exec`, `internal/router/exec_track.go`; isolated shell fixtures in `internal/router/exec_track_test.go`, sandbox acceptance in `internal/router/app_server_exec_track_codex_e2e_test.go` |
| Reserved MCP endpoint lifetime and stdio bridge | `libraries/mcptransport`; explicit `mcp-bridge` dispatch in `apps/mekugi/cmd/mekugi/main.go`; no normal-startup endpoint |
| Agent roster presentation | `internal/router/native_roster.go`; native assignments in `internal/router/native_activity_assignments.go` |
| Saved Diff caller filtering and capture attribution | `internal/router/live_diff_data.go`, `internal/router/mekugi_store.go`; composition in `internal/livediff/model.go` |
| Inactive instruction-file loading | `modules/contextreset`; supplied-history decoding in `codexbinding`, bounded source collection in `guidance`; no production caller |
| Projected frontend guidance | `modules/agentguidance/codexbinding` renders selected command descriptions and module-owned `guidance.md` contributions; `internal/router/tool_registry.go` authenticates and publishes the invocation snapshot. |
| Inherited instruction cleanup and request-phase integration | `modules/agentguidance`; `internal/router/request_guidance.go`, `internal/router/instruction_cache_test.go`, `internal/router/stock_guidance_workflow_test.go`; recorded stock fixtures in `internal/router/testdata/instruction-cleanup/`, contract in `modules/agentguidance/SPEC.md` |
| AX runtime evidence and offline measurements | `modules/agentmetrics/evidence`; authenticated reader dispatch in `internal/router/tool_plugin_worker.go` |
| Offline logical session inspection | `modules/sessioninspection/query` owns retained joins and findings; its `codexbinding` interprets host identities and execution evidence. `contracts/inspection` collects optional AX and provider usage; application CLI dispatch remains in `internal/router/session_inspect.go` and `session_corpus.go`. |
| Offline UI replay and profiling | `internal/router/session_ui_replay.go`; interface in `doc/spec/session_replay.md`, usage in `docs/contributing/development.md` |
| Live session and offline replay performance profiling | `internal/router/profiling.go`, `internal/router/profiling_disabled.go`; diagnostic binary recipe in `Makefile` |
| Observed review capture and host outcomes | `internal/router/native_apply_patch.go`, `internal/router/exec_observation.go`, `internal/router/app_server_shell_edits.go`; pure review model in `libraries/diffmodel` |
| Retained change identities, review, frozen selections, apply/revert, and retirement | `modules/changehistory`; publication and live-view adapters in `internal/router/change_history.go` |
| Retained publication, namespaces, allocation, leases, and retention | `modules/sessionhistory/history`; composed record kinds in `apps/mekugi/internal/composition`, feature codecs in `internal/router/retained_record_kinds.go` |
| Historical call-envelope decoding and snapshot validation | `libraries/legacycall`; router conversion in `internal/router/legacy_call.go` |
| Completed command-segment publication, compatibility, and adoption | `modules/commandsegments/reports`; host-fact and presentation integration in `internal/router/command_segments.go`; explicit readonly projection in `internal/router/storage_inspect.go` |
| Durable replay meaning, request-visible history, and retained output | `internal/router/mekugi_store.go`, `internal/router/mekugi_history.go`, `modules/outputrecovery` |
| Authenticated compiled frontend catalog, worker, and PATH | `internal/router/tool_registry.go`, `internal/router/tool_plugin_worker.go`, `internal/router/tool_wrapper.go`, `internal/runtimepath` |
| Symbol command arguments and activity display | `modules/symbolquery/query` owns parsing; `internal/router/subagent_tool_display_shell.go` consumes it |
| Shared frontend options, mcat selection grammar, and literal shell quoting | `libraries/commandargs`; readers, activity display, offline inspection and shell instrumentation share argument semantics; content reading and multi-target delivery remain with their callers |
| Bounded foreground command execution | `modules/boundedcommand`; authenticated dispatch in `internal/router/tool_plugin_worker.go` |
| Structural inspection | `modules/sourceoutline`; shared declarations and source coordinates in `libraries/sourceanalysis/structure` |
| Raw source reading and shared output admission | `modules/fileread`; `libraries/outputbudget`; retained publication and paging belong to `modules/outputrecovery` |
| Router process signals, wrapped Codex lifecycle, and top-level exit | `apps/mekugi/cmd/mekugi/main.go`, `apps/mekugi/cmd/mekugi/wrap.go` |
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
