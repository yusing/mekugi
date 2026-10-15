# Mekugi owner map


Paths are relative to the repository root. Start with the matching production entrypoint;
use its focused tests when choosing validation. These are navigation pointers, not a second
behavioral contract. UI presentation packages and router session integration are separate owners.

| Behavior | Authoritative area |
| --- | --- |
| Workspace enumeration and import-boundary validation | `apps/mekugi/cmd/workspace`; membership in `go.work` |
| Application composition and initial executable bindings | `apps/mekugi/internal/composition`, `contracts`, `backends/codex/binding`; scripted harness in `testsupport/moduletest` |
| Review-diff rendering used by observed change evidence | Root-package `review*.go` |
| Shared source capability and shell-header semantics | `libraries/sourceanalysis/sourcekind`, `libraries/commandanalysis/shellsyntax` |
| Shared source parsing, literal decoding, and nonblocking source-file opens | `libraries/sourceanalysis/sourceparse`; execution classification uses `internal/execscope`, while preview evaluation remains in `internal/router` |
| Activity presentation, diff navigation/previews, and terminal primitives | `internal/ui/activity`, `internal/ui/diffview`, `internal/ui/terminal`; native session integration remains in `internal/router` |
| Native screen composition, transcript switching, and pane layout | `internal/router/native_shell.go`; preview fixtures in `internal/router/app_server_preview_test.go` |
| Session/composer controller and host-event reconciliation | `internal/router/app_server_ui.go`; input paths in `internal/router/app_server_input.go`; composer error display in `internal/router/composer_notice.go` |
| Source-aware selection, scrolling, and logical copy | `internal/router/terminal_selection.go`, `internal/router/terminal_source_selection_test.go`, `internal/router/terminal_selection_scroll_test.go` |
| Native approval dialog, Codex approval responses, and approval-mode thread policy | `internal/router/app_server_approvals.go`; question dock in `internal/router/app_server_questions.go` |
| Sudo command approval and secret password input | `internal/sudoask`, `apps/mekugi/cmd/mekugi-exec/sudo.go`, `internal/router/sudo.go`; approval dock/session grants in `internal/router/app_server_approvals.go`, command instrumentation in `internal/vcsguard/rewrite.go`, Bash startup in `internal/vcsguard/shell.go`, invocation setup in `apps/mekugi/cmd/mekugi/wrap.go`; contract in `doc/spec/execution.md` REQ-EXECUTION-004 |
| Remote VCS write guard: classification, command instrumentation, and approval hand-off | `internal/vcsguard` (native command hook protocol in `hook.go`, shared VCS/sudo instrumentation definitions in `rewrite.go`, display projection in `display.go`, startup setup in `shell.go`; consumers: `internal/router/subagent_tool_display.go` and `internal/router/exec_track.go`), `apps/mekugi/cmd/mekugi-exec/guard*.go`, `internal/router/vcs_guard.go`; session hook registration in `apps/mekugi/cmd/mekugi/vcs_guard_hook.go`, local hook dispatch in `apps/mekugi/cmd/mekugi-exec/hook.go`, effective-hook checks in `internal/router/app_server_guard_hook.go`, shell environment in `apps/mekugi/cmd/mekugi/wrap.go`; contract in `doc/spec/execution.md` REQ-EXECUTION-003 |
| Running-process composer count and `/ps` dialog | `internal/router/running_processes.go`; paging and live output in the shared `internal/router/output_dialog.go`; live commands remain in the app-server session |
| Dialog integration, Markdown file links, and background fading | `internal/router/output_dialog.go`, `internal/router/markdown_file_dialog.go`, `internal/ui/activity/dialog.go`, `internal/ui/activity/faint.go` |
| Shared Markdown and activity painting | `internal/ui/activity/paint.go`; rendering coverage in `internal/ui/activity/*snapshot_test.go` |
| Submitted file attachments | `internal/router/composer_file_attachment.go`; shared path formatting in `libraries/pathdisplay` |
| Live-diff models and preview presentation | `internal/livediff/model.go`, `internal/ui/diffview/preview.go`; native composition in `internal/router/native_shell.go` |
| Selected-file editor handoff | `internal/router/native_file_editor.go`, `internal/router/terminal_ui.go`; shared editor runner and terminal restoration in `internal/router/app_server_composer_editor.go`, `internal/router/app_server_ui.go` |
| Shared Diff syntax and word highlighting | `internal/livediff/render.go`, `internal/livediff/word_diff.go`, `internal/livediff/theme.go`; source previews in `internal/ui/diffview/preview.go` |
| Codex app-server stdio RPC and child-process lifecycle | `internal/appserver` |
| Router lifecycle, launch flags, modes, and HTTP endpoints | `internal/router/server.go`, `internal/router/flags.go` |
| Invocation-local Mekugi TOML configuration and service-tier selection | `internal/router/config.go`, `internal/router/service_tier.go`; OpenCode credentials remain in `internal/router/opencode_config.go` |
| Third-party native-agent projection, Grok authentication, and model metadata | `internal/router/subagent_bridge.go`, `internal/router/grok_*.go` |
| Provider request encoding, stream normalization, and Responses lifecycle emission | `internal/providercodec`; resolved provider policy and transport remain in `internal/router/provider_boundary.go`, `internal/router/grok_client.go`, and `internal/router/opencode.go` |
| Automatic notices and root-visible child activity | `internal/router/critical_errors.go`, `internal/router/subagent_activity.go`; boundaries in `doc/architecture/notices.md` and `doc/architecture/activity.md` |
| Per-thread token/cost reports and final-answer stream ordering | `internal/router/thread_usage.go`, `internal/router/token_cost.go`, `internal/router/final_answer_stream.go` |
| Codex-facing WebSocket sessions, incremental history, and steering | `internal/router/server_websocket.go` |
| Codex authentication and upstream Responses transport | `internal/router/client.go`, `internal/router/client_websocket.go` |
| Provider-measured output throughput and response-byte receipt timing | `internal/router/output_throughput.go`, `internal/router/thread_usage_store.go`, `internal/router/stream_diagnostics.go`; capture snapshots remain in `capturer` |
| Stock tool preservation and response observation | `internal/router/mekugi_proxy.go`, `internal/router/mekugi_response_transform.go`, `internal/router/native_apply_patch.go` |
| Shell/source execution-scope classification and bounded provider queries | `internal/execscope`; host observation and retained-change resolution remain in `internal/router/exec_observation.go` and `internal/router/exec_changes.go` |
| Model-visible duplicate output references | `internal/router/output_dedupe.go`; content matching in `internal/outputdedupe`; local projection metrics in `capturer/output_dedupe.go`; provider-prefix reuse in `internal/router/provider_history.go` |
| Shell segment reports and measured command timing | `internal/execsegment`, `apps/mekugi/cmd/mekugi-exec`, `internal/router/exec_track.go`; isolated shell fixtures in `internal/router/exec_track_test.go`, sandbox acceptance in `internal/router/app_server_exec_track_codex_e2e_test.go` |
| Reserved MCP endpoint lifetime and stdio bridge | `internal/router/mcp_server.go`, `internal/router/mcp_bridge.go`; no normal-startup consumer |
| Agent roster presentation | `internal/router/native_roster.go`; native assignments in `internal/router/native_activity_assignments.go` |
| Saved Diff caller filtering and capture attribution | `internal/router/live_diff_data.go`, `internal/router/mekugi_store.go`; composition in `internal/livediff/model.go` |
| Inactive instruction-file loading | `internal/router/context_reset_guidance.go` and platform file-open helpers; no production caller |
| Projected frontend guidance | Source: `guidance/frontend_guidance.md.tmpl`; generated output: `internal/router/frontend_guidance.md`; embedding: `internal/router/frontend_guidance.go`. Generation/check: `TestGeneratedFrontendGuidanceIsCurrent` in `internal/router/frontend_guidance_generation_test.go`. Executable tool sources own their descriptions. |
| Inherited instruction conflict cleanup and prepared-request role projection | `internal/router/instruction_conflicts.go`, `internal/router/instruction_cache_test.go`, `internal/router/stock_guidance_workflow_test.go`; recorded stock fixtures in `internal/router/testdata/instruction-cleanup/`, contract in `doc/spec/guide.md` |
| AX runtime evidence and offline measurements | `capturer/ax.go`; authenticated reader dispatch in `internal/router/tool_plugin_worker.go` |
| Offline logical session inspection | `internal/router/session_inspect.go`, dispatched by `apps/mekugi/cmd/mekugi/main.go` |
| Offline UI replay and profiling | `internal/router/session_ui_replay.go`; interface in `doc/spec/session_replay.md`, usage in `docs/contributing/development.md` |
| Live session and offline replay performance profiling | `internal/router/profiling.go`, `internal/router/profiling_disabled.go`; diagnostic binary recipe in `Makefile` |
| Observed review diffs, change IDs, and bounded reads | `review.go`, `internal/router/native_apply_patch.go`, `internal/router/exec_observation.go`, `internal/router/app_server_shell_edits.go`, `internal/router/mekugi_changes.go`, `internal/router/mchanges.go` |
| Durable replay, request-visible history, and retained output | `internal/router/mekugi_store.go`, `internal/router/mekugi_history.go`, `internal/router/shell_output_read.go` |
| Authenticated compiled frontend catalog, worker, and PATH | `internal/router/tool_registry.go`, `internal/router/tool_plugin_worker.go`, `internal/router/tool_wrapper.go`, `internal/runtimepath` |
| Symbol command arguments and activity display | `internal/router/toolplugin/native_symbol.go` owns parsing; `internal/router/subagent_tool_display_shell.go` consumes it |
| Shared frontend options, token ceilings, and inclusive range parsing | `internal/router/toolplugin/frontend_options.go`; consumers include native readers and `internal/router/shell_read_bundle.go`; command-specific acceptance and multi-target framing remain with their callers |
| Built-in tool sources and output tokenization | `internal/router/toolplugin/native_*.go`; compiled catalog in `internal/router/toolplugin/native_builtins.go` |
| Router process signals, wrapped Codex lifecycle, and top-level exit | `apps/mekugi/cmd/mekugi/main.go`, `apps/mekugi/cmd/mekugi/wrap.go` |
| Normative interface requirements | `doc/spec/index.md` and the listed requirement file |
| Stable ownership contracts | `doc/architecture/index.md` and the listed contract file |

## Session evidence and UI performance

For questions about what an agent actually did, start with recorded session calls and results;
current source and contracts explain intended behavior, not historical execution. Codex rollouts
live under `$CODEX_HOME/sessions`, defaulting to `~/.codex/sessions`. Locate an exact session ID
with a filename glob using `rg --files --hidden --no-ignore` in that directory, then verify
`session_meta` identity and workspace. Do not count child sessions or inherited fork history as
independent user sessions. The logical inspection interface is in `doc/spec/session.md`.

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
