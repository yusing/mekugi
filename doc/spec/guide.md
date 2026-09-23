# Agent guidance

## REQ-GUIDE-001 — Current agent guidance

Mekugi preserves the caller's base `instructions` value and every system or developer message,
except for complete explicit omission blocks and pinned inherited stock-prompt conflicts.
It does not detect stock prompt shapes, replace editing sections, select model-specific workflows,
inspect `model_instructions_file`, or create or modify instruction files. It does not restore the
retired shell carrier, HPATCH, hash-target editing, or CTP instructions.

Exact inherited stock fragments that conflict with the current journal, planning, or wait
workflow are rewritten in top-level instructions and developer text parts. Unrelated policy,
system messages, user content, non-text parts, and fenced examples remain unchanged. Rewriting is
idempotent and model-independent. Execution-free requests retain their native guidance.
Pinned planning fragments require decision completeness rather than a quota of questions;
the pinned short-wait fragment yields to completion notifications or interruptible waits.
These replacements match complete lines and preserve caller-added qualifications.

Each executable helper's tool source owns its description. A template outside router produces a
checked-in standalone Markdown file with XML-framed built-in frontends and named guidance used by
router tools. Router embeds the generated file without keeping instruction prose in Go. Configured
plugin descriptions come from the authenticated registry and are appended to the frontend section
as XML-framed entries. That session frontend section is also written into the pinned registry
snapshot and appended to the authoritative Code Mode
`exec` description when it exposes `tools.exec_command`, or to native `exec_command`. It does not publish a second catalog or
change stock tool names, schemas, inputs, results, or execution authority. Refresh replaces the
marked section in place; malformed or duplicate markers reject before forwarding. The session guide
combines the embedded generated built-in section with current pinned plugin descriptions, not a
model-specific prompt file or routing-session ID.

The projected helper guidance explains when the tools reduce work, not just their syntax:
structural outlines for navigation, semantic references for caller-impacting edits, bounded
command windows and retained-output recovery, and explicit captured-change ranges for reviewers.
CLI help includes concrete invocation examples and consequential option interactions, including
inclusive file ranges and line limits within those ranges. A smaller window is incomplete
evidence, not coverage of the full requested range.
Default token budgets are 6000 for file reads (`mcat`), 8000 for retained-output continuation
(`mread`), and 4000 for symbol queries and structural outlines. Explicit token limits override
these defaults up to 15500; line-only reads retain their existing no-token-limit behavior.
It preserves context reuse and batching of ready related edits without replacing the stock editor.
Review handoffs include the requested scope and explicit IDs or same-agent inclusive ranges;
the recipient's own change listing cannot discover another agent's IDs. Historical edit evidence
does not assert the current workspace state or cover shell-generated changes.

The journal owner supplies the additive durable-work guidance. The dedicated
`functions.journal` description explains milestone scope, immediate reporting, Code Mode mutation,
and finish semantics. Eligible structured tools receive the optional atomic `journal` mutation
field. For Code Mode, Mekugi appends one marked Journal section to the authoritative `exec` tool
description without removing the caller's stock execution contracts.
The section points to the dedicated journal tool description rather than repeating its rules.
That description owns Code Mode mutation syntax, batching, and completion conditions for both
native and Code Mode consumers. A previously marked section is refreshed in place. Duplicate, incomplete, or reversed
markers reject before forwarding instead of creating ambiguous guidance.

Journal projection is rebuilt from the current request's authenticated tool catalog. It does not
depend on a routing-session ID, a live parent, an earlier prompt rewrite, or a particular model.
Ordinary turns, forks, side threads, subagents, model switches, compaction continuations, and resumed
threads therefore receive the same current guidance when they expose the applicable tool owner.
Native tool requests receive the dedicated journal description and session-helper guidance on
`exec_command`, without changing their other stock tools.
Execution-free and prewarm requests retain their existing lifecycle rules.

Before provider forwarding, the router strips blocks enclosed by the exact HTML comments
`<!-- mekugi:omit -->` and `<!-- /mekugi:omit -->`, including both comments. This is an explicit
caller-authored filtering contract, distinct from pinned conflict rewriting. It applies to top-level instructions,
system and developer message text, and the `<INSTRUCTIONS>` body of Codex user-role instruction
context, including AGENTS.md. Ordinary user messages, assistant history, tool results, and non-text
parts remain intact. Each text part is processed independently, including fenced text; nested and
multiple complete blocks are supported. Unmatched markers and bytes outside complete blocks are
preserved. Filtering also applies to prewarm and execution-free requests.

In Mekugi mode, when `skills-mgr` is executable in the wrapped Codex PATH, the wrapper enforces the
invocation-local Codex setting `skills.include_instructions=false`, so the stock skill catalog is not
duplicated alongside the skills-mgr catalog. The router replaces explicitly selected Codex skill
instructions with the compact identity `<skill name="…"/>` before provider forwarding. This
idempotent projection applies to ordinary, prewarm, execution-free, and replayed selected-skill
messages without modifying Codex configuration files. In passthrough mode or when `skills-mgr` is
unavailable, both stock catalog instructions and selected-skill messages remain unchanged.

Acceptance:

1. Stock, custom, missing, null, top-level, and developer-carried base instructions are forwarded
   unchanged except for complete explicit omission blocks and exact inherited conflict fragments.
   Fenced examples and non-instruction content are not rewritten.
2. Code Mode receives exactly one current marked Journal section and, when it exposes command
   execution, one registry-derived frontend section in its authoritative `exec` description.
   Native `exec_command` receives the frontend
   section. Refresh is idempotent, malformed markers fail closed, and unrelated descriptions,
   sibling tools, and stock execution contracts remain unchanged.
3. The dedicated journal tool and optional mutation field expose enough guidance to record concise
   milestones, request immediate reporting, list retained items, and finish without another provider
   request or separate final answer.
4. Ordinary, fork, side-thread, subagent, model-switch, compaction, and resume consumers derive
   guidance from their current tool catalog and authenticated registry rather than invisible ancestry
   or live router state. Each helper has one description owner; the built-in section of the
   checked-in generated Markdown matches the built-in projection, and the session copy matches
   the complete projected frontend section.
5. Omission filtering preserves unmatched markers and all bytes outside complete owned blocks.
6. In Mekugi mode with `skills-mgr` in PATH, Codex launches disable stock skill-catalog instructions
   after caller overrides and selected skill wrappers reach the provider as `<skill name="…"/>`.
   In passthrough mode or without `skills-mgr`, neither projection occurs.

## REQ-GUIDE-002 — Native post-compaction recovery

Main threads recover useful durable facts after compaction without a model-driven
journal/change lookup. The wrapper registers a native Codex `SessionStart` command
hook matching `compact`, using invocation-local configuration. Codex owns event
timing, trust review, execution, context insertion, and hook-output history. Native
`PostCompact` is not the injection surface because it does not expose additional
context. Subagents and passthrough sessions are outside this feature's scope.

The hook reads the retained journal and executing-thread-owned change evidence
through their existing owners. It includes journal IDs, authors, questions, delivery
state, retained change ranges, and aggregated numstat, not full diffs. These are
historical facts, not new authorization or proof of current workspace state. No
tools or effects are replayed, and listing does not acknowledge journal delivery.

Recovery uses the native event's stable session identity and absolute workspace,
not a live router, parent process, routing-session ID, or process cwd. Missing or
conflicted identity and unavailable storage produce advisory failure, never an
invented empty success. Existing hooks and user configuration remain owned by
Codex; explicit CLI hooks configuration takes precedence over auto-registration.
Trust is never bypassed. Native hook disablement remains effective.

Acceptance:

1. Manual and automatic root compaction invoke the same `SessionStart` hook;
   its output reaches the immediate model continuation without another model call.
2. Restart and resume read the same durable records. Forks read their own journal
   and own change attempts; unrelated workspaces, threads, and children cannot
   contribute records to that snapshot.
3. Journal and change selection share a locked snapshot. Each section is bounded
   to 8 KiB with UTF-8-safe truncation and explicit retrieval instructions. Removed,
   partial, or unavailable change evidence retains the change owner's limitations.
4. Non-compact events do nothing. Child identities do not inject context. Invalid
   input and storage errors are advisory native hook failures; they neither stop
   the turn nor claim successful recovery.
5. Automatic registration preserves file-based hooks, does not write configuration
   files, and leaves explicit CLI hooks untouched with a visible registration notice.
