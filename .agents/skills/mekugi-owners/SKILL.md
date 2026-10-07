---
name: mekugi-owners
description: Use when locating the authoritative Mekugi code or documentation owner for a behavior.
user-invocable: false
metadata:
  internal: true
---

## Owners

Paths are relative to the repository root. Start with the matching production entrypoint;
use its focused tests when choosing validation. These are navigation pointers, not a second
behavioral contract. UI presentation packages and router session integration are separate owners.

| Behavior | Authoritative area |
| --- | --- |
| Review-diff rendering used by observed change evidence | Root-package `review*.go` |
| Shared quoted operands, logical rows, source capability, Go lexical, and shell-header semantics | `internal/quotedoperand`, `internal/logicalrow`, `internal/sourcekind`, `internal/golex`, `internal/shellsyntax` |
| Versioned plugin shared-core adapter and private WASM bridge | `internal/router/toolplugin/core-v1.mjs`, `internal/router/toolplugin/core-v1.d.ts`, `internal/sharedwasm` |
| Activity presentation, diff navigation/previews, and terminal primitives | `internal/ui/activity`, `internal/ui/diffview`, `internal/ui/terminal`; native session integration remains in `internal/router` |
| Native screen composition and pane layout | `internal/router/native_shell.go`; preview fixtures in `internal/router/app_server_preview_test.go` |
| Session/composer controller and host-event reconciliation | `internal/router/app_server_ui.go`; input paths in `internal/router/app_server_input.go`; composer error display in `internal/router/composer_notice.go` |
| Source-aware selection, scrolling, and logical copy | `internal/router/terminal_selection.go`, `internal/router/terminal_source_selection_test.go`, `internal/router/terminal_selection_scroll_test.go`; logical row semantics in `internal/logicalrow` |
| Native approval dialog, Codex approval responses, and approval-mode thread policy | `internal/router/app_server_approvals.go`; question dock in `internal/router/app_server_questions.go` |
| Remote VCS write guard: classification, command instrumentation, and approval hand-off | `internal/vcsguard` (hook protocol in `hook.go`, source instrumentation in `rewrite.go`, startup setup in `shell.go`), `cmd/mekugi-exec/guard*.go`, `internal/router/vcs_guard.go`; session hook registration in `cmd/mekugi/vcs_guard_hook.go`, effective-hook checks in `internal/router/app_server_guard_hook.go`, shell environment in `cmd/mekugi/wrap.go`; contract in `doc/spec/execution.md` REQ-EXECUTION-003 |
| Dialog integration, Markdown file links, and background fading | `internal/router/output_dialog.go`, `internal/router/markdown_file_dialog.go`, `internal/ui/activity/dialog.go`, `internal/ui/activity/faint.go` |
| Shared Markdown and activity painting | `internal/ui/activity/paint.go`; rendering coverage in `internal/ui/activity/*snapshot_test.go` |
| Submitted file attachments | `internal/router/composer_file_attachment.go`; shared path formatting in `internal/pathdisplay` |
| Journal cards and agent roster presentation | `internal/router/native_journal.go`, `internal/router/journal_card.go`, `internal/router/native_roster.go`; durable journal state remains separate |
| Live-diff models and preview presentation | `internal/livediff/model.go`, `internal/ui/diffview/preview.go`; native composition in `internal/router/native_shell.go` |
| Claude native events into shared presentation | `internal/router/native_runtime_session.go`, `internal/router/native_runtime_activity.go`, `internal/router/native_runtime_tasks.go`; native transport in `internal/claude`; interface in `doc/spec/native_ui.md` |
| Claude streamed edit proposals | Native prefix decoder: `internal/claude/preview.go`, `internal/claude/events.go`; shared Bash worker adapter: `internal/router/native_runtime_command_preview.go`, using `live_diff_preview.go` and `live_diff_events.go`; Edit/Write adapter: `native_runtime_preview.go`. Native PTY: `native_runtime_command_preview_claude_native_test.go`, `native_runtime_preview_continuity_claude_native_test.go` in `internal/router`; offline state and rendered cases: `NativeRuntimeCommandPreview`, `NativeRuntimePreview`, `UISnapshotNativeRuntimeCommandPreview`. |
| Claude child conversation viewing | SDK forwarding: `internal/claude/bridge/bridge.ts`; parent-tool identity: `internal/claude/events.go`; shared roster/filter/stop adapter: `internal/router/native_runtime_tasks.go`; native PTY acceptance: `internal/router/native_runtime_agents_claude_native_test.go`; offline shared rendering: `TestUISnapshotNativeRuntimeParity`. |
| Claude direct child messages | Shared `/to` command and roster-target completion: `internal/router/native_runtime_messages.go`, using `app_server_picker.go` and `native_runtime_input.go`; official invocation-local mod carrier: `internal/claude/bridge/agent_messages.ts`; saved native targets: `internal/claude/bridge/session_history.ts`; native API acceptance: `internal/claude/native_agent_messages_e2e_test.go`; shared PTY: `internal/router/native_runtime_agents_claude_native_test.go`; offline interaction and rendered cases: `NativeRuntimeMessages`, `UISnapshotNativeRuntimeMessages`. |
| Claude side questions | Shared dock: `internal/router/app_server_btw.go`; native event/input adapter: `internal/router/native_runtime_btw.go`; isolated SDK fork: `internal/claude/bridge/side_query.ts`; native PTY acceptance: `internal/router/native_runtime_btw_claude_native_test.go`; offline state and rendered cases: `NativeRuntimeBTW`, `UISnapshotNativeRuntimeBTW`. |
| Claude retaining user-shell shortcut | Shared Shell Mode: `internal/router/app_server_shell.go`, `internal/router/native_runtime_shell.go`; native input, retention and shutdown/resume: `internal/claude/bridge/user_shell.ts`, `internal/claude/bridge/bridge.ts`; saved native receipts: `internal/claude/user_shell.go`. Native API and independent-side acceptance: `internal/claude/native_user_shell_e2e_test.go`; shared PTY: `internal/router/native_runtime_shell_claude_native_test.go`; offline state/history/rendering: `NativeRuntimeShell`, `NativeShellCarriers`, `UISnapshotNativeRuntimeShell`; bridge shutdown-failure coverage: `internal/claude/bridge/user_shell.test.ts`. |
| Claude shared session controls | Native controller receipts: `internal/router/native_runtime_controls.go`; shared titles and saved picker: `internal/router/app_server_session_title.go`, `internal/router/app_server_resume_picker.go`; native SDK controls: `internal/claude/bridge/bridge.ts`, `internal/claude/bridge/session_controls.ts`; drained scope and companion rotation: `internal/router/runtime_session_switch.go`, `internal/router/runtime_companion.go`. Native acceptance: `internal/router/native_runtime_controls_claude_native_test.go`; full title/picker/clear/resume/failure/cancellation PTY: `internal/router/native_runtime_controls_pty_test.go`; cross-workspace picker/fresh-owner resume: `internal/router/native_runtime_cross_workspace_claude_native_test.go`. Offline controller, scope and rendered cases: `native_runtime_controls_test.go`, `native_runtime_title_test.go`, `runtime_session_switch_test.go` in `internal/router`. |
| Claude native command output | Native tail polling and terminal spool delivery: `internal/claude/bridge/task_output.ts`; typed events: `internal/claude/events.go`; shared retention and dialogs: `internal/ui/activity/output.go`, `internal/router/native_runtime_activity.go`; immutable spool retention and history restore: `internal/router/native_runtime_output_store.go`, using `internal/router/shell_output_store.go`. Native interaction acceptance: `internal/router/native_runtime_output_pty_test.go`; offline chunk/restart coverage: `internal/router/native_runtime_output_test.go`. |
| Claude native command segments | Literal wrapper adapter: `internal/execsegment/claude.go`; shared shell observer: `internal/execsegment/hook.go`, `cmd/mekugi-exec/main.go`; native registration and shared UI consumption: `internal/router/runtime_exec_track.go`, `internal/router/runtime_observation.go`, `internal/router/exec_track.go`. Completed replay: `internal/router/command_segments.go`, `internal/router/native_runtime_segments.go`; native child/fork selections: `internal/router/runtime_history.go`, `internal/claude/bridge/session_history.ts`. Native acceptance: `internal/router/runtime_exec_track_claude_native_test.go`, `internal/router/native_runtime_segments_claude_native_test.go`; gated segment/dialog/cancellation PTY: `internal/router/native_runtime_segments_pty_test.go`; rendered fixtures and differential checks: `internal/router/runtime_exec_track_test.go`. |
| Native tool observation and Bash capture | `internal/router/runtime_observation.go`, `internal/router/runtime_observation_service.go`; native hooks in `internal/claude/bridge/companion.ts`; interface in `doc/spec/changes.md` |
| Claude journal and tool-guidance delivery | Workflow source: `internal/router/claude_companion_skill.md`; invocation-local generation: `internal/router/runtime_companion.go`; bounded loading: `internal/claude/bridge/guidance.ts`; preset and native hook delivery: `internal/claude/bridge/bridge.ts`, `internal/claude/bridge/companion.ts`. Native MCP receipts and large-read output: `internal/router/runtime_journal.go`. Contract: `doc/spec/guide.md`. Native prompt/adoption entrypoints: `runtime_guidance_claude_native_test.go`, `runtime_journal_adoption_claude_live_test.go` in `internal/router`. |
| Shared Diff syntax and word highlighting | `internal/livediff/render.go`, `internal/livediff/word_diff.go`, `internal/livediff/theme.go`; source previews in `internal/ui/diffview/preview.go` |
| Codex app-server stdio RPC and child-process lifecycle | `internal/appserver` |
| Router lifecycle, launch flags, modes, and HTTP endpoints | `internal/router/server.go`, `internal/router/flags.go` |
| Third-party native-agent projection, Grok authentication/translation, and model metadata | `internal/router/subagent_bridge.go`, `internal/router/grok_*.go` |
| Automatic notices and root-visible child activity | `internal/router/commentary.go`, `internal/router/commentary_publisher.go`, `internal/router/subagent_activity.go`; boundaries in `doc/architecture/notices.md` and `doc/architecture/journal.md` |
| Per-thread token/cost reports and final-answer stream ordering | `internal/router/thread_usage.go`, `internal/router/token_cost.go`, `internal/router/final_answer_stream.go` |
| Codex-facing WebSocket sessions, incremental history, and steering | `internal/router/server_websocket.go` |
| Codex authentication and upstream Responses transport | `internal/router/client.go`, `internal/router/client_websocket.go` |
| Provider-measured output throughput and response-byte receipt timing | `internal/router/output_throughput.go`, `internal/router/thread_usage_store.go`, `internal/router/stream_diagnostics.go`; capture snapshots remain in `capturer` |
| Stock tool preservation and response observation | `internal/router/mekugi_proxy.go`, `internal/router/mekugi_response_transform.go`, `internal/router/native_apply_patch.go` |
| Model-visible duplicate output references | `internal/router/output_dedupe.go`; content matching in `internal/outputdedupe`; provider-prefix reuse in `internal/router/provider_history.go` |
| Shell segment reports and measured command timing | `internal/execsegment`, `cmd/mekugi-exec`, `internal/router/exec_track.go`; isolated shell fixtures in `internal/router/exec_track_test.go`, sandbox acceptance in `internal/router/app_server_exec_track_codex_e2e_test.go` |
| Journal state, router-owned CRUD, terminal delivery, and replay | `internal/router/journal.go`, `internal/router/journal_tool.go`, `internal/router/journal_delivery.go` |
| Journal continuation/reset and recovery text | `internal/router/journal_reset_driver.go`, `internal/router/journal_compaction.go`, `internal/router/journal_summary.go` |
| Journal writing rules and projected agent guidance | Prose source: `guidance/frontend_guidance.md.tmpl`; generated output: `internal/router/frontend_guidance.md`; embedding: `internal/router/frontend_guidance.go`; journal consumer: `internal/router/journal_tool.go`. Generation/check: `TestGeneratedFrontendGuidanceIsCurrent` in `internal/router/frontend_guidance_generation_test.go`. Frontend tool descriptions remain owned by their executable tool sources. |
| Inherited instruction conflict cleanup and prepared-request role projection | `internal/router/instruction_conflicts.go`, `internal/router/instruction_cache_test.go`, `internal/router/stock_guidance_workflow_test.go`; recorded stock fixtures in `internal/router/testdata/instruction-cleanup/`, contract in `doc/spec/guide.md` |
| AX runtime evidence and offline measurements | `capturer/ax.go`; authenticated reader dispatch in `internal/router/tool_plugin_worker.go` |
| Offline logical session inspection | `internal/router/session_inspect.go`, dispatched by `cmd/mekugi/main.go` |
| Offline UI replay and profiling | `internal/router/session_ui_replay.go`; interface in `doc/spec/session_replay.md`, usage under README's "Replay a session" |
| Live session and offline replay performance profiling | `internal/router/profiling.go`, `internal/router/profiling_disabled.go`; diagnostic binary recipe in `Makefile` |
| Observed review diffs, change IDs, and bounded reads | `review.go`, `internal/router/native_apply_patch.go`, `internal/router/mekugi_changes.go`, `internal/router/mchanges.go` |
| Durable replay, request-visible history, and retained output | `internal/router/mekugi_store.go`, `internal/router/mekugi_history.go`, `internal/router/shell_output_read.go` |
| Authenticated frontend registry, worker, and PATH | `internal/router/tool_registry.go`, `internal/router/tool_plugin_worker.go`, `internal/router/tool_wrapper.go`, `internal/runtimepath` |
| Built-in tool sources, output tokenization, and plugin runtime | `internal/router/toolplugin/native_*.go`; plugin loading and embedded assets in `internal/router/toolplugin/runtime.go` |
| Router process signals, wrapped Codex lifecycle, and top-level exit | `cmd/mekugi/main.go`, `cmd/mekugi/wrap.go` |
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
