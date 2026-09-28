---
name: mekugi-owners
description: Use when locating the authoritative Mekugi code or documentation owner for a behavior.
user-invocable: false
metadata:
  internal: true
---

## Owners

| Behavior | Authoritative area |
| --- | --- |
| Review-diff rendering used by observed change evidence | Root-package `review*.go` |
| Shared quoted operands, logical rows, source capability, Go lexical, and shell-header semantics | `internal/quotedoperand`, `internal/logicalrow`, `internal/sourcekind`, `internal/golex`, `internal/shellsyntax` |
| Versioned plugin shared-core adapter and private WASM bridge | `internal/router/toolplugin/core-v1.mjs`, `internal/router/toolplugin/core-v1.d.ts`, `internal/sharedwasm` |
| Activity presentation, diff navigation/previews, terminal primitives, and dashboard | `internal/ui/activity`, `internal/ui/diffview`, `internal/ui/terminal`, `internal/ui/dashboard`; native session integration remains in `internal/router` |
| Codex app-server stdio RPC and child-process lifecycle | `internal/appserver` |
| Router lifecycle, launch flags, modes, and HTTP endpoints | `internal/router/server.go`, `internal/router/flags.go` |
| Third-party native-agent projection, Grok authentication/translation, and model metadata | `internal/router/subagent_bridge.go`, `internal/router/grok_*.go` |
| Automatic notices and root-visible child activity | `internal/router/commentary.go`, `internal/router/commentary_publisher.go`, `internal/router/subagent_activity.go`; details in `doc/architecture/commentary.md` |
| Per-thread token/cost reports and final-answer stream ordering | `internal/router/thread_usage.go`, `internal/router/token_cost.go`, `internal/router/final_answer_stream.go` |
| Mentor Handoff model schedule | `internal/router/mentor_handoff.go` |
| Codex-facing WebSocket sessions, incremental history, and steering | `internal/router/server_websocket.go` |
| Codex authentication and upstream Responses transport | `internal/router/client.go`, `internal/router/client_websocket.go` |
| Stock tool preservation and response observation | `internal/router/mekugi_proxy.go`, `internal/router/mekugi_response_transform.go`, `internal/router/native_apply_patch.go` |
| Journal state, router-owned CRUD, terminal delivery, and replay | `internal/router/journal.go`, `internal/router/journal_tool.go`, `internal/router/journal_delivery.go` |
| AX runtime evidence and offline measurements | `capturer/ax.go`; authenticated reader dispatch in `internal/router/tool_plugin_worker.go` |
| Offline logical session inspection | `internal/router/session_inspect.go`, dispatched by `cmd/mekugi/main.go` |
| Observed review diffs, change IDs, and bounded reads | `review.go`, `internal/router/native_apply_patch.go`, `internal/router/mekugi_changes.go`, `internal/router/mchanges.go` |
| Durable replay, request-visible history, and retained output | `internal/router/mekugi_store.go`, `internal/router/mekugi_history.go`, `internal/router/shell_output_read.go` |
| Authenticated frontend registry, worker, and PATH | `internal/router/tool_registry.go`, `internal/router/tool_plugin_worker.go`, `internal/router/tool_wrapper.go`, `internal/runtimepath` |
| Built-in tool sources, output tokenization, and plugin runtime | `plugins`, `internal/router/toolplugin` |
| Router process signals, wrapped Codex lifecycle, and top-level exit | `cmd/mekugi/main.go`, `cmd/mekugi/wrap.go` |
| Normative interface requirements | `doc/spec/index.md` and the listed requirement file |
| Stable ownership contracts | `doc/architecture/index.md` and the listed contract file |
