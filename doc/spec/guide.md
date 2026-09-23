# Agent guidance

## REQ-GUIDE-001 — Current agent guidance

Mekugi preserves the caller's base `instructions` value and every system or developer message,
except for complete explicit omission blocks and pinned inherited stock-prompt conflicts.
It does not detect stock prompt shapes, replace editing sections, select model-specific workflows,
inspect `model_instructions_file`, or create or modify instruction files. It does not restore the
retired shell carrier, HPATCH, hash-target editing, or CTP instructions.

Exact inherited stock fragments that conflict with the current journal or planning
workflow are rewritten in top-level instructions and developer text parts. Unrelated policy,
system messages, user content, non-text parts, and fenced examples remain unchanged. Rewriting is
idempotent and model-independent. Execution-free requests retain their native guidance.

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

The journal owner supplies the additive durable-work guidance. The dedicated
`functions.journal` description explains milestone scope, immediate reporting, Code Mode mutation,
and finish semantics. Eligible structured tools receive the optional atomic `journal` mutation
field. For Code Mode, Mekugi appends one marked Journal section to the authoritative `exec` tool
description without removing the caller's stock execution contracts.
The section documents `await journal(...)`, the ordinary-call field preference, and the requirement
to call `functions.journal` finish alone after required results rather than emitting a separate final
answer. A previously marked section is refreshed in place. Duplicate, incomplete, or reversed
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
