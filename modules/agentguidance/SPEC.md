# Agent guidance

## Purpose and scope

Assemble contributed frontend guidance and project explicit omissions, pinned stock
conflicts, and selected-skill identities without changing caller authority.

## Behavior

Mekugi preserves the caller's base `instructions` value and every system or developer message,
except for complete explicit omission blocks, pinned inherited stock-prompt conflicts, and the
marked Mekugi workflow section appended to base instructions.
It does not detect stock prompt shapes, replace editing sections, select model-specific workflows,
inspect `model_instructions_file`, or create or modify instruction files. It does not restore the
retired shell carrier, HPATCH, hash-target editing, or CTP instructions.

Exact inherited stock progress conflicts are deleted, and the pinned wait conflict is
rewritten in top-level instructions and developer text parts. Pinned stock paragraphs lose
their commentary directives while retaining permission and task-steering policy. Complete stock
uninstalled-plugin advertisements are removed. Inherited delegation and custom plugin policy
remain intact. Unrelated policy,
system messages, user content, non-text parts, and fenced examples remain unchanged. Rewriting is
idempotent and model-independent. Execution-free requests retain their native guidance.
The pinned short-wait fragment yields to completion notifications or interruptible waits.
Stock line and paragraph matches preserve caller-added qualifications. Planning
guidance and the stock plan tool remain host-owned. Invocation-local Codex flags omit
collaboration-mode instructions.

Each executable helper owns its capability description and command-keyed workflow
guidance. Descriptions contain capabilities, syntax, options and output semantics;
workflow guidance explains when and how to use the helper during a task. Absent
command contributions contribute neither description nor workflow guidance. The
module renders both from the validated invocation catalog, without a duplicate
checked-in catalog. The application authenticates and publishes both sections
before exposing its commands.

Capability descriptions remain in the authoritative `exec` description when it
exposes `tools.exec_command`. Workflow guidance is appended to the Responses `instructions` field, preserving
any existing value. Codex's selected base text remains unchanged when carried as
a developer input message; Mekugi does not infer its identity from message order
or wording. The workflow section is part of the request's base instruction layer,
not conversation history or a tool description. Stock names, schemas,
inputs, results, and execution authority stay unchanged. Refresh replaces each
marked section in place; malformed or duplicate markers reject before forwarding.

The base workflow section explains when the tools reduce work:
structural outlines for navigation, semantic references for caller-impacting edits, bounded
command windows and retained-output recovery, and explicit captured-change ranges for reviewers.
Capability descriptions include support for ordinary shell pipes.
On macOS, the execution guidance directs agents to select Bash explicitly unless
the task requires another shell. This leaves Codex's account-shell detection and
user shell commands unchanged.
CLI help includes concrete invocation examples and consequential option interactions, including
inclusive file ranges and line limits within those ranges. A smaller window is incomplete
evidence, not coverage of the full requested range.
Default token budgets are 6000 for file reads (`mcat`), 8000 for retained-output continuation
(`mread`), and 4000 for symbol queries and structural outlines. Explicit token limits override
these defaults up to 15500; `mcat -n` retains its default token ceiling.
It preserves context reuse and batching of ready related edits without replacing the stock editor.
Review handoffs include the requested scope and explicit IDs or same-agent inclusive ranges;
the recipient's own change listing cannot discover another agent's IDs. Historical edit evidence
does not assert the current workspace state.

The invocation's `child_reply_summary` feature conditionally contributes a rule
inside the existing marked workflow section. Enabled children author substantive
child-to-parent directed messages and final answers with a concise, meaningful
`## Summary` and supporting `## Details` in the same existing reply generation.
The summary states the actual result, findings or blocker and material uncertainty,
normally in one to three sentences. Brief acknowledgements may remain plain;
higher-priority exact-output requirements and root ordinary answers are unchanged.
There is no separate summary message, repair turn or extra model request.
Disabled invocations omit only this rule. Authenticated assembly applies the
current invocation value once to fresh/no-history children, forks, switches and
resumed requests, preserving caller text outside the owned workflow section.

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

Native-composer file and skill frames identify their supplied content as reusable
read context, so agents need only read sources for edits or missing/newer content.
Skill snapshots provide the actual selected instructions through the attachment
projection, resolving managed metadata placeholders through `skills-mgr` even for
user-invoked skills omitted from its model catalog. Matching standalone Codex skill
injections, including compact name references, are omitted from provider input only
when a successful snapshot supplies that source's instructions. Native matches retain the metadata
path; managed snapshots take precedence by name. Omission notices do not suppress
Codex's available instructions. Unrelated messages and quoted
examples remain unchanged; Codex still owns its selection and execution.

## Lifecycle, Codex connection, records, and failure

The module is stateless, with no records or background resources. Its binding
registers early omission filtering, execution-only conflict projection, late
selected-skill projection, frontend assembly, and marked-description and base-workflow replacement.
Omissions run before preparation, including compaction and execution-free requests.
Execution conflicts retain their existing preparation position; unsupported prewarm
handshakes bypass them. Attachment matching precedes selected-skill compaction.
Projection errors stop forwarding. With zero guidance contributions, the module
still handles its instruction policies and descriptions of selected commands.
Removing the module removes all its projections and assembly; command execution
remains available. Provider compatibility prose belongs to its backend consumers.

## Acceptance

1. Stock, custom, missing, null, top-level, and developer-carried base instructions are forwarded
   unchanged except for complete explicit omission blocks, exact inherited conflict fragments,
   and the marked base-workflow section.
   Fenced examples and non-instruction content are not rewritten.
2. When `exec` exposes command execution, its authoritative description receives one
   registry-derived capability section, while base instructions receive the workflow section.
   Workflow rules do not remain in tool descriptions. Refresh is idempotent; malformed markers reject
   before forwarding. Unrelated descriptions, sibling tools, and stock execution
   contracts remain unchanged.
3. Ordinary, fork, side-thread, subagent, model-switch, compaction-continuation, and resume
   requests derive frontend guidance from their authenticated catalog. Guidance does
   not depend on a live parent or earlier prompt rewrite. Supported prewarm catalogs
   receive the same frontend section as generating requests.
4. Each helper has one description owner. The invocation guide includes the selected
   descriptions exactly once and excludes absent command supplements. Changed valid
   descriptions render directly; the snapshot and projected bytes match.
5. Omission filtering preserves unmatched markers and all bytes outside complete owned blocks.
6. In Mekugi mode with `skills-mgr` in PATH, Codex launches disable stock skill-catalog instructions
   after caller overrides and selected skill wrappers reach the provider as `<skill name="…"/>`.
   In passthrough mode or without `skills-mgr`, neither projection occurs.
7. Compaction uses the host/provider path without a recovery-injection hook.
