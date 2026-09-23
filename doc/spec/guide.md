# Agent guidance

## REQ-GUIDE-001 — Additive tool guidance

Mekugi preserves the caller's base `instructions` value and every system or developer message,
except for complete explicit omission blocks described below.
It does not detect stock prompt shapes, replace editing sections, rewrite conflicting prose, select
model-specific workflows, inspect `model_instructions_file`, or create or modify instruction files.
Tool projection may add call-local guidance only to tool descriptions and schemas owned by Mekugi.

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
Native tool requests receive the dedicated journal description without changing their base prompt.
Execution-free and prewarm requests retain their existing lifecycle rules.

Before provider forwarding, the router strips blocks enclosed by the exact HTML comments
`<!-- mekugi:omit -->` and `<!-- /mekugi:omit -->`, including both comments. This is an explicit
caller-authored filtering contract, not prompt rewriting. It applies to top-level instructions,
system and developer message text, and the `<INSTRUCTIONS>` body of Codex user-role instruction
context, including AGENTS.md. Ordinary user messages, assistant history, tool results, and non-text
parts remain intact. Each text part is processed independently, including fenced text; nested and
multiple complete blocks are supported. Unmatched markers and bytes outside complete blocks are
preserved. Filtering also applies to prewarm and execution-free requests.

Acceptance:

1. Stock, custom, missing, null, top-level, and developer-carried base instructions are forwarded
   unchanged except for complete explicit omission blocks.
2. Code Mode receives exactly one current marked Journal section in its authoritative `exec`
   description. Refresh is idempotent, malformed markers fail closed, and unrelated description
   content and sibling tools remain unchanged.
3. The dedicated journal tool and optional mutation field expose enough guidance to record concise
   milestones, request immediate reporting, list retained items, and finish without another provider
   request or separate final answer.
4. Ordinary, fork, side-thread, subagent, model-switch, compaction, and resume consumers derive
   guidance from their current tool catalog rather than invisible ancestry or live router state.
5. Omission filtering preserves unmatched markers and all bytes outside complete owned blocks.
