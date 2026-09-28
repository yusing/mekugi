# Mekugi-owned UI over Codex app-server

Status: implementation authorized; roughly 35–40% delivered. Settled decisions,
ownership, scope and implemented behavior live in the
[native app-server contract](../spec/router.md#native-app-server-preview). This
file tracks only the remaining work and is deleted when the replacement gate
passes. Each working increment pauses for user review.

| Stage | State |
| --- | --- |
| 1. Protocol feasibility | Start, stream, steer, interrupt and child observation done; explicit-ID startup resume done; questions, resume selection, fork and Code Mode completion remain. |
| 2. Shared view and Explored | Shared renderer done; Explored grouping remains. |
| 3. Usable Main | Composer, steering, queueing and interrupt done; pickers, questions, new/fork, resume picker/switching and effective model status remain. |
| 4. Native publication | Activity from app-server and native journals done; native notices and removal of old carriers remain. |
| 5. Metrics view | Not started. |
| 6. Cutover | Not started; live validation not started. |

## Codex behavior to port

Port user-visible semantics through app-server and the shared renderer, not Rust
widgets.

| Behavior | Remaining decision |
| --- | --- |
| Text entry | History, per-thread drafts, path/image input. |
| Interrupt and exit | Ctrl-C in an auxiliary pane returns to Main. Never kill app-server to interrupt. |
| User-input questions | Supported separately from approvals, keyed by thread and request identity; stale responses are suppressed after resolution or interruption. |
| Streaming transcript | Errors, cancellation, partial output and plan items (without re-enabling `update_plan`). |
| New/resume/fork | Explicit-ID startup resume, pane layout/focus, historical roster/Activity and retained Diff content are implemented. Resume picker, `--last`, in-session switching, paginated hydration beyond the 16 MiB transport cap remain; new and fork use thread operations, and fork keeps inherited history. |
| Model, effort, mode | Existing catalogs and routing owners; show configured and effective values separately. |
| Slash commands | A small explicit set mapped to actions, plus help. |

### `$skills` and `@file` pickers

Typing the trigger opens filtered candidates; arrows select; Tab/Enter accept;
Escape dismisses and keeps the draft. Show loading, empty, and error states;
ignore stale results; keep `$HOME`-style text. Skill discovery reuses Codex's
catalog even when model-visible catalog instructions are off. File selection
inserts a quoted path and never auto-reads content or invents attachments.

Picker state is UI state. Provider requests keep the current skills policy
([REQ-GUIDE-001](../spec/guide.md), [selected-skill projection](../../internal/router/skill_instructions.go)):
with `skills-mgr` in Mekugi mode, `skills.include_instructions=false` and selected
skills project to `<skill name="…"/>`; passthrough and non-`skills-mgr` runs keep
stock behavior. Acceptance checks both the picker and captured provider requests.
MentionsV2 plugin/app/task catalogs are out of scope.

## Shared Explored grouping

Consecutive eligible exploration operations collapse into one group, `Exploring…`
while running and `Explored` when settled; expanded details keep every operation,
target, and output reference. Main and Activity already draw consecutive
operations as a tree; grouping adds the settled state and collapse.

- Eligibility comes from typed classification: read, search, list, inspect,
  skill read. Execution, mutation, and unknown operations stay visible and split
  groups. A command is not exploration because its text contains `cat` or `rg`.
- Groups stay within one thread, turn, and uninterrupted segment. Messages,
  mutations, approvals, turn boundaries, and another author's visible activity
  split them; filtering never merges across hidden boundaries.
- A group runs until every member settles; yielded commands are still running.
  Failures stay visible with a collapsed count. Members update by call identity,
  so retries and duplicate observations do not double-count.
- Collapse verbosity, not evidence. Resumed history groups only with equivalent
  structured evidence.

Codex implements grouping in its TUI, not app-server. Applying it to the agents
log is an intentional extension. Reasoning events are not group boundaries.
The explore output filter remains separate and claims no savings from fewer rows.

## Retiring Codex-rendering commentary

Activity, assignment and message mirrors, author prefixes, pane notices and
journal updates are already native. Remaining carriers:

| Carrier | Decision |
| --- | --- |
| Critical errors and delivery/capacity notices | Native notices with the same diagnostic identity, dedup, and ack; never consumed undelivered. |
| Usage and Mentor information | Existing metrics/roster/report owners; the usage report stays. |

Migration conditions:

- One sink per user-only publication, scoped to its originating thread: native
  UI for attached sessions, the inline carrier where a noninteractive path needs
  it. No global flag and no concurrent render paths.
- Publish at the existing owner, never by scraping generated Markdown. Establish
  the native receipt path before disabling the old emitter; renderer failure
  leaves work pending.
- Remove only synthetic envelopes, not host-required finals, substantive answers,
  child audience content, or raw-answer fallback.
- Keep exact-ID provenance stripping for old generated messages on resume/fork.
- After validation, delete emitters, the collector's pane path, and tests that
  exist only for Codex rendering.

## Event delivery and recovery

- Correlate by Codex thread/turn/item IDs plus explicit Mekugi observation IDs;
  keep separate provenance when a join is unproven. Each item has one lifecycle
  authority.
- Buffer while establishing a snapshot; slow renderers get a gap indicator and
  never backpressure execution. Hidden views keep receiving state.
- Switching views is local, not a new subscription. Display is not a journal
  acknowledgement.
- Reconnect to a surviving backend by reconciling snapshot, active turn, and
  pending requests. Process death marks work interrupted, keeps drafts, and offers
  restart/resume; ambiguous submissions are never resent.

Launch compatibility still to confirm against the [launcher](../../cmd/mekugi/wrap.go)
and [Mentor scheduling](../spec/mentor.md): preserved environment, private PATH,
provider selection, `include_collaboration_mode_instructions=false`, journal-mode
`update_plan` disablement, conditional skill instructions, catalog bootstrap,
credential isolation, post-compaction hooks and their trust rules; terminal
restoration and exit summaries survive shutdown.

## Metrics view and cutover

Replace the metrics HTTP API and dashboard HTML with a switchable in-frontend
Metrics view reading metrics state in-process. Remove `/api/metrics`, dashboard
serving and assets, and the startup URL, with no replacement HTTP or SSE API.
Switching views preserves focus, draft, scroll, and selection; collection
continues. Capture and export stay.

Cutover makes the client the interactive default and removes Main's PTY
forwarding and the wrapped-terminal shell, keeping emulator code the diff pane
needs. Before cutover, validate with live runs, not source inspection:

- Identical Explored behavior in both panes across mixed operations, merges,
  parallel calls, failures, yields, interleaving, filtering, duplicates, resume.
- Paste, Unicode, attachments, pickers, drafts, steer/queue races,
  interrupt/exit, question routing, no double submission.
- Provider projection unchanged with and without `skills-mgr` and in passthrough.
- Fork/resume, roster selection, model switches, compaction, router restart,
  without cross-thread leakage or duplicate journal delivery.
- View switching, event gaps, process death, pending requests, narrow layouts,
  terminal restoration.
- Tools execute once; edits keep durable evidence; usage stays provider-
  authoritative; capture, `--debug` artifacts, and the usage report survive the
  dashboard removal.
- Full answers stay reachable after clipping; journal events arrive without an
  open provider response, render once, and stay pending on failure.
- Event volume, redraws, memory, and snapshot size are measured.

At each stage, move settled behavior into the metrics, launch, commentary,
journal delivery, and change presentation contracts and the affected README
sections, and remove it here.

## References

Read-only evidence: Codex checkout `86be5320` (inspected 2026-09-25–26) and the
Mekugi sources linked above. The main feasibility risk is event coverage and
identity correlation for native children and nested Code Mode operations.

- [App-server overview](https://github.com/openai/codex/blob/86be5320/codex-rs/app-server/README.md)
  and [documentation](https://learn.chatgpt.com/docs/app-server)
- [Protocol](https://github.com/openai/codex/blob/86be5320/codex-rs/app-server-protocol/src/protocol/common.rs),
  [turn controls](https://github.com/openai/codex/blob/86be5320/codex-rs/app-server-protocol/src/protocol/v2/turn.rs),
  [thread lifecycle](https://github.com/openai/codex/blob/86be5320/codex-rs/app-server/src/request_processors/thread_lifecycle.rs),
  [server requests](https://github.com/openai/codex/blob/86be5320/codex-rs/app-server/src/outgoing_message.rs),
  [Code Mode host](https://github.com/openai/codex/blob/86be5320/codex-rs/app-server/src/code_mode_host.rs)
- Exploration [model](https://github.com/openai/codex/blob/86be5320/codex-rs/tui/src/exec_cell/model.rs),
  [rendering](https://github.com/openai/codex/blob/86be5320/codex-rs/tui/src/exec_cell/render.rs),
  [history grouping](https://github.com/openai/codex/blob/86be5320/codex-rs/tui/src/thread_transcript/exploration_groups.rs)
- [Composer queue](https://github.com/openai/codex/blob/86be5320/codex-rs/tui/src/chatwidget/input_queue.rs),
  [approval routing](https://github.com/openai/codex/blob/86be5320/codex-rs/tui/src/app/app_server_requests.rs),
  [reconnect](https://github.com/openai/codex/blob/86be5320/codex-rs/tui/src/app/reconnect.rs)
- [Skill discovery](https://github.com/openai/codex/blob/86be5320/codex-rs/tui/src/chatwidget/skills.rs),
  [composer selection](https://github.com/openai/codex/blob/86be5320/codex-rs/tui/src/bottom_pane/chat_composer.rs),
  [input serialization](https://github.com/openai/codex/blob/86be5320/codex-rs/tui/src/chatwidget/input_submission.rs)
