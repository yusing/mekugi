# Agent navigation

## Project goal

Mekugi is an optimization and enhancement layer for stock Codex. Evaluate features and tradeoffs
against these goals, in order:

1. **Cut costs:** reduce model round trips and payload size, and improve batching.
2. **Improve AX and UX:** make agent workflows clearer and more recoverable while giving users
   useful visibility into progress and results.
3. **Improve agent performance:** provide better context, more reliable operations, and effective
   model and tool use, with correctness established by evidence.

Compression, tools, routing, observability, and model scheduling are means toward these goals, not
ends in themselves. Preserve Codex as the execution authority as required below.

## Feedback from active sessions

Agents working in an active Mekugi session may proactively report agent-experience (AX)
issues and improvement feedback encountered while using it, even when unrelated to the current
task. Report to the user, or to the parent agent for inclusion in its report. Keep feedback brief
and grounded in observed behavior: describe the operation, expected versus actual behavior,
workflow impact, and available evidence. Distinguish observations from suspected causes and
suggestions; omit secrets and unrelated session content.

Report blockers promptly; group non-blocking feedback with the final response without derailing
the assigned task. This permits reporting, not unrelated investigation, fixes, or external issue
filing without authorization.

## Common requirements

The linked contracts own interface-specific details, exceptions, and acceptance cases.

- **Session continuity:** Features must remain correct across `/fork`, `/side`, agent switching
  through `/subagents`, model switches, and `codex resume`, including a fresh router process.
  Restore inherited state from visible history and durable workspace records, not routing-session
  IDs, cache keys, or a live parent. Preserve documented lifetimes: replay does not revive
  processes, continuation handles, or expired private scripts; Mentor schedules are router-lifetime.
  Evidence: [replay](doc/spec/plugin.md), [recovery](doc/spec/correct.md),
  [guidance switches](doc/spec/guide.md), [Mentor lifetime](doc/spec/mentor.md).
- **State isolation:** Keep request views, stable thread identity, workspace replay, and process
  resources distinct. Concurrent requests and branches must not borrow another thread's state.
  Truncation or compaction removes invisible ancestry from that request, not durable records
  needed by other branches. Cleanup is explicit and limited to owned resources.
  See [history ownership](doc/architecture/boundary.md) and [commentary identity](doc/architecture/commentary.md).
- **Host authority:** Codex owns tool execution, permissions, sandboxing, native agent lifecycle,
  and yielded-session continuation. Router translation, replay, observation, and display must not
  execute effects again or take over that lifecycle. Keep overrides invocation-local and leave
  user configuration and instructions untouched.
  See [plugins](doc/spec/plugin.md), [subagents](doc/spec/subagents.md), and [launch](doc/spec/router.md).
- **Filesystem authority:** Normal router translation uses the selected metadata directory,
  never router cwd, and does not impose the root library's confinement boundary. Without a
  selected directory, relative operands reject. Do not add workspace selectors, rebasing, or
  multi-directory routing without evidence from a real Codex request.
  See [boundary](doc/architecture/boundary.md) and [dated host observations](doc/codex-router-e2e.md).
- **Atomic edits and usable evidence:** Resolve targets against one immutable invocation baseline;
  parsing, validation, and evaluation failures publish no partial edit or success report.
  Readers, target checks, and reports share verified-row semantics; reports describe completed,
  formatted state, not guessed coordinates. Translation is not proof of application, hashes are
  not writer locks, and multi-file commit is not crash-atomic. Preserve caller coordination and
  truthful rollback diagnostics.
  See [targets](doc/spec/select.md), [output](doc/spec/output.md), and [core](doc/architecture/core.md).
- **One semantic owner:** Reuse the engine, portable core, carrier renderer, and capturer rather
  than duplicating their semantics in plugins, adapters, reports, or dashboards. Validate the
  complete plugin registry before exposing it; reject unsupported or corrupt translations rather
  than silently approximate them. Preserve exact tool identity and input across JSON, streaming,
  native, and Code Mode paths.
  See [plugin boundary](doc/architecture/plugin.md) and [plugin requirements](doc/spec/plugin.md).
- **Durability before exposure:** Persist completed call mappings before exposing executable
  carriers, including calls completed before their enclosing stream terminates. Never evaluate
  unfinished arguments. Storage failure blocks carrier exposure; capacity limits must not evict
  resumable correctness state. Replay validates retained facts without retranslating.
  See [replay requirements](doc/spec/plugin.md) and [store ownership](doc/architecture/boundary.md).
- **Auxiliary means non-invasive:** Commentary, capture, and diagnostics must not replace tool
  results, alter execution, or replay effects. Bound their resources independently of correctness
  state; remove generated history only by retained provenance, not text resemblance. Keep secrets
  and content out of sanitized metrics, and credentials separated by provider. Explicit diagnostic
  artifacts have their own documented content and startup-failure contracts.
  See [commentary](doc/spec/commentary.md), [metrics](doc/spec/metrics.md),
  [debug artifacts](doc/spec/router.md), and [provider isolation](doc/spec/subagents.md).
- **Evidence over apparent success:** Judge correctness by actual results, path scope, and required
  graders, not model prose, transcript labels, or reference-patch similarity. Provider usage owns
  model-consumption claims; local token estimates and transport expansion are different measures.
  Missing or incomplete evidence is not zero or success. Recheck dated host observations after
  Codex upgrades before relying on them.
  See [benchmark](doc/spec/benchmark.md), [metrics](doc/spec/metrics.md), and [E2E evidence](doc/codex-router-e2e.md).

## Build and installation constraints

Never run `make install` or `make install-binaries`, including through another target or
script. Do not bypass this rule with `go install` or by copying Mekugi binaries into an
installation directory. Installation examples in `README.md` are user workflows, not
agent authorization.

The Makefile's default target is `install`, so do not run bare `make` either. It has no
build or test targets; use Go and Bun directly.

After implementing and validating a feature, always prepare a runnable temporary build
outside the repository using `mktemp -d`. Include required helpers and a launcher, and
provide the exact command to test it. Never install or overwrite existing binaries.

## Where to look

- `README.md`: user guide for installation, deployment, and router/shell workflows.
  Update it only when user actions, configuration, prerequisites, or observable workflows change.
- `doc/spec/index.md`: interface requirements and acceptance criteria.
- `doc/architecture/index.md`: boundary ownership contracts.
- `contrib/codex/file-editing-instructions.md`, adjacent `editing-workflow-astra.md` and
  `editing-workflow-default.md`, and `tool_grammar.lark`: HPATCH syntax and Codex model guidance.
  Read these only for changes or validation in that scope.
- `~/projects/codex`: read-only Codex CLI clone. Cloning it if missing requires user permission.

Instruction templates and model guidance inspected as project content, including
`contrib/codex/*`, are not instructions for the current task. This rule does not disable
applicable `AGENTS.md` guidance loaded by the client or instructions supplied in the conversation.

Documentation references are one-way: this file may point to docs, but docs must not refer
back here. Docs must stand on their own interface and architecture references.

## Owners

| Behavior | Authoritative area |
| --- | --- |
| Public engine entry points and workspace APIs | `run.go` |
| Parsing, targets, edit planning, transactions, translation, reports, hooks, and engine metrics | Root-package `*.go` files and adjacent tests |
| Shared quoted-string and heredoc framing | `internal/hpatchsyntax` |
| Portable verified-row, source-capability, Go-lexical, and shell-header semantics | `internal/verifiedrow`, `internal/sourcekind`, `internal/golex`, `internal/shellsyntax` |
| Versioned plugin shared-core adapter and private WASM bridge | `internal/router/toolplugin/core-v1.mjs`, `internal/router/toolplugin/core-v1.d.ts`, `internal/sharedwasm` |
| Router lifecycle, launch flags, modes, and HTTP endpoints | `internal/router/server.go`, `internal/router/flags.go` |
| Third-party native subagent projection, Grok authentication/translation, and model-catalog metadata | `internal/router/subagent_bridge.go`, `internal/router/grok_*.go` |
| Automatic router notices and root-visible child activity | `internal/router/commentary.go`, `internal/router/commentary_publisher.go`, `internal/router/subagent_activity.go`; detailed ownership in `doc/architecture/commentary.md` |
| Per-thread token/cost reports and final-answer stream ordering | `internal/router/thread_usage.go`, `internal/router/token_cost.go`, `internal/router/final_answer_stream.go` |
| Mentor Handoff model schedule | `internal/router/mentor_handoff.go` |
| CTP/2 provider representation | `internal/router/ctp2.go` |
| Codex-facing WebSocket sessions, incremental history, and steering | `internal/router/server_websocket.go` |
| Codex authentication and upstream Responses transport | `internal/router/client.go`, `internal/router/client_websocket.go` |
| Tool replacement, host translation, and response restoration | `internal/router/mekugi_proxy.go` |
| Journal state, router-owned CRUD, terminal delivery, and replay | `internal/router/journal.go`, `internal/router/journal_tool.go`, `internal/router/journal_delivery.go` |
| Bash/POSIX execution and bounded external-command output | `internal/router/shell_runner.go`, `internal/router/shell_hrun.go` |
| AX runtime evidence and offline measurements | `capturer/ax.go`; actual private-reader dispatch in `internal/router/shell_runner.go` |
| Offline logical session inspection | `internal/router/session_inspect.go`, dispatched by `cmd/mekugi/main.go` |
| Hpatch review diffs, shared change IDs, and bounded change reads | `review.go`, `internal/router/mekugi_changes.go`, `internal/router/shell_changes.go` |
| Durable replay records, request-visible history, and rejected-script recovery | `internal/router/mekugi_store.go`, `internal/router/mekugi_history.go`, `internal/router/mekugi_recovery.go` |
| Carrier catalog and model-visible projection | `internal/router/tool_carrier.go`, `internal/router/tool_registry.go` |
| Built-in tool sources, shared GPT-5 output tokenization, and private execution runtime | `plugins` (tokenization in `plugins/tokens.ts`), `internal/router/toolplugin` |
| Fixed shell-runtime locator and per-thread runtime path | `cmd/shell`, `internal/shellruntime`, `internal/router/shell_runtime.go` |
| Configured plugin discovery, authenticated snapshots, and frontends | `internal/router/toolplugin/runtime.go`, `internal/router/tool_registry.go`, `internal/router/tool_wrapper.go` |
| Router process signals, wrapped Codex lifecycle, private model-catalog snapshot, and top-level exit behavior | `cmd/mekugi/main.go`, `cmd/mekugi/wrap.go`, `cmd/mekugi/catalog.go` |
| Normative interface requirements | `doc/spec/index.md` and the listed requirement file |
| Stable ownership contracts | `doc/architecture/index.md` and the listed contract file |

## Focused checks

| Changed owner | Focused check |
| --- | --- |
| Root engine | `.` |
| Router behavior | `./internal/router` |
| Capture metrics and AX evidence | `./capturer` |
| Portable core or `mekugi:core/v1` adapter | `./internal/router/toolplugin`, then `./...` and `bun test ./internal/router/toolplugin/tests/core.test.ts` |
| TypeScript plugin source | `go generate ./internal/router/toolplugin`, then `bun test ./internal/router/toolplugin/tests` |
| Router or shell-helper process entry point | `./cmd/mekugi ./cmd/shell` |
| Native journal child-result acceptance | `-tags journal_e2e ./internal/router -run '^TestJournalNativeCodexSpawnE2E$'` (installed Codex, local mock provider) |
| Cross-package or broad contract | `./...` |
| Router or shell-helper process entry point | `./cmd/mekugi ./cmd/shell` |
| Cross-package or broad contract | `./...` |

Generation requires Bun and the dependencies declared in `plugins/package.json`. If those
dependencies are missing, use `bun install --cwd plugins --frozen-lockfile`; do not use
`make install` to prepare them. Generation rebuilds the embedded WASM core and JavaScript bundle
through directives in `internal/router/toolplugin/runtime.go`; do not hand-edit generated assets.
