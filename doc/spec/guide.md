# Agent guidance

## REQ-GUIDE-001 — Current agent guidance

Mekugi preserves the caller's base `instructions` value and every system or developer message,
except for complete explicit omission blocks and pinned inherited stock-prompt conflicts.
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
guidance is not rewritten: invocation-local Codex flags disable the plan tool and
collaboration-mode instructions.

Each executable helper's tool source owns its description. A template outside router produces a
checked-in standalone Markdown file with XML-framed built-in frontends and named guidance used by
router tools. Router embeds the generated file without keeping instruction prose in Go. Configured
plugin descriptions come from the authenticated registry and are appended to the frontend section
as XML-framed entries. That session frontend section is also written into the pinned registry
snapshot and appended to the authoritative `exec` description when it exposes
`tools.exec_command`. It does not publish a second catalog or
change stock tool names, schemas, inputs, results, or execution authority. Refresh replaces the
marked section in place; malformed or duplicate markers reject before forwarding. The session guide
combines the embedded generated built-in section with current pinned plugin descriptions, not a
model-specific prompt file or routing-session ID.

The projected helper guidance explains when the tools reduce work, not just their syntax:
structural outlines for navigation, semantic references for caller-impacting edits, bounded
command windows and retained-output recovery, and explicit captured-change ranges for reviewers.
It also notes that frontend commands support ordinary shell pipes.
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
does not assert the current workspace state or cover shell-generated changes.

The journal owner supplies the additive durable-work guidance once per request: on the
`exec` in one marked Journal section. No dedicated
`functions.journal` tool is exposed. Journal access uses the invocation-local
MCP tools; stock tool schemas and inputs remain unchanged.
The guidance covers journal use and completion, without prescribing delegation, review,
document workflow or personal setup. Tasks track work that needs continuation;
short assignments need no plan. Main guidance plans an outcome task's separate results as
child tasks. Main guidance covers work updates, recovery facts, slice
boundaries, and work-report completion. Subagent guidance covers interim facts and final
reports. Both versions use the same native MCP schemas and shared journal semantics
for stable paths, reads, batching and failures. Main work updates belong in the journal; requested answers
and necessary questions remain conversational. Subagents record useful interim facts and may deliver their
final report directly. A result already recorded in the journal is not repeated
as a second completion report.
The section describes journal MCP calls through Codex's nested tools.
Projection preserves the caller's stock execution contracts. A
previously marked section is refreshed in place. Duplicate, incomplete, or reversed markers
reject before forwarding instead of creating ambiguous guidance.

Journal projection is rebuilt from the current request's authenticated tool catalog
and Codex turn metadata. Subagent turns receive the shorter policy, including
nested and unnamed subagents; other turns receive Main policy. Selection does not
depend on a routing-session ID, a live parent, an earlier prompt rewrite, or a
particular model. Ordinary turns, forks, side threads, model switches, compaction
continuations and resumed threads receive current role-appropriate guidance when
they expose the applicable tool owner.
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

Acceptance:

1. Stock, custom, missing, null, top-level, and developer-carried base instructions are forwarded
   unchanged except for complete explicit omission blocks and exact inherited conflict fragments.
   Fenced examples and non-instruction content are not rewritten.
2. `exec` receives exactly one current marked Journal section and, when it exposes command
   execution, one registry-derived frontend section in its authoritative `exec` description.
   Refresh is idempotent, malformed markers fail closed, and unrelated descriptions,
   sibling tools, and stock execution contracts remain unchanged.
3. The execution-tool journal section and MCP schemas expose enough guidance to record concise
   tasks and facts, read retained subtrees, and finish naturally with an answer.
   Journal format uses ASD-STE100, short titles with no trailing punctuation, a bold
   action where useful, and one topic per item. Bodies hold detail. Slice guidance retains
   bounded checkable results; guidance specifies no generic development workflow.
   Main work turns use the finish marker in the final useful execution when host results
   establish completion, without a follow-up `Done.` acknowledgment or another
   provider request. A usable deliverable, usage explanation or needed decision
   beyond the work report uses a natural final answer. Subagents may return a final
   report without creating a journal task.
   Short assignments omit plans, role-restating tasks and duplicate clean-review
   results. When their final report is already in the journal, they use the finish
   marker when host results establish completion. Requested explanations, review
   findings and actual questions remain conversational. Guidance directs mutations
   onto useful calls rather than standalone journal calls; `exec` uses the nested MCP tools for reads and mutations.
4. Ordinary, fork, side-thread, subagent, model-switch, compaction, and resume consumers derive
   guidance from their current tool catalog and authenticated registry rather than invisible ancestry
   or live router state. Prewarm and generating requests select the same role policy;
   refresh replaces stale opposite-role policy without changing shared API types
   or stock execution. Each helper has one description owner; the built-in section of the
   checked-in generated Markdown matches the built-in projection, and the session copy matches
   the complete projected frontend section.
5. Omission filtering preserves unmatched markers and all bytes outside complete owned blocks.
6. In Mekugi mode with `skills-mgr` in PATH, Codex launches disable stock skill-catalog instructions
   after caller overrides and selected skill wrappers reach the provider as `<skill name="…"/>`.
   In passthrough mode or without `skills-mgr`, neither projection occurs.

## REQ-GUIDE-002 — Native post-compaction recovery

Main threads receive a current-work handoff after compaction without a model-driven
lookup to assemble it. Full detail remains available through selective journal/change
reads. It is on by default in mekugi mode; `--post-compact-recovery=false`
opts out. The wrapper registers a native Codex `SessionStart` command hook matching
`compact`, using invocation-local configuration. Codex owns event timing, execution,
context insertion, and hook-output history. Native
`PostCompact` is not the injection surface because it does not expose additional
context. Subagents and passthrough sessions are outside this feature's scope.

For router-answered compaction, the hook emits nothing only when the latest
compacted rollout response ID matches the durable thread/workspace synthesis
record. Missing, malformed or mismatched transcript evidence keeps ordinary
recovery. This check survives router restart and never acknowledges journal events.

The hook reads the retained journal and executing-thread-owned change evidence
through their existing owners. V2 uses the [evidence-backed journal summary](journal.md#evidence-backed-recovery),
including open task paths and states, a context-path index, bounded root-scope,
resume-branch and current-work bodies, and changes/failures captured after the
last journal event. Recovery names the
selected workspace without Git queries. File guidance the thread loaded
before the reset follows it as
[retained guidance](journal.md#retained-guidance). Its short read hint explains how to retrieve omitted detail, discover
older own paths and find older agents. Child content is
grouped once under a readable agent heading with child-local paths, which readers
can select through `agent` and `view:"own"`. Closed work and
history remain readable rather than being indexed in every handoff. Retained v1
journals include IDs, authors, questions and delivery
state. Both include retained change ranges, not full diffs. V2 adds aggregated numstat
only for changes after the last journal event; v1 includes it for all retained changes. These are
historical facts, not new authorization or proof of current workspace state. No
tools or effects are replayed, and listing does not acknowledge journal delivery.

Recovery uses the native event's stable session identity and absolute workspace,
not a live router, parent process, routing-session ID, or process cwd. Missing or
conflicted identity and unavailable storage produce advisory failure, never an
invented empty success. Existing hooks and user configuration remain owned by
Codex; explicit CLI hooks configuration takes precedence over auto-registration.
The wrapper pre-trusts only its own registered hook, through invocation-local hook
state carrying that hook's exact Codex trust hash; global trust bypass is never used,
and every other hook keeps native trust review. A hash Codex no longer computes
identically leaves the hook under native review. Native hook disablement remains
effective.

Acceptance:

1. Manual and automatic root compaction invoke the same `SessionStart` hook;
   its output reaches the immediate model continuation without another model call.
2. Restart and resume read the same durable records. Forks read their own journal
   and own change attempts; unrelated workspaces, threads, and children cannot
   contribute records to that snapshot.
3. Journal and evidence selection share a locked snapshot. V2 follows the journal
   summary bounds; legacy sections are bounded to 8 KiB with UTF-8-safe truncation
   and explicit retrieval instructions. Removed,
   partial, or unavailable change evidence retains the change owner's limitations.
4. Non-compact events do nothing. Child identities do not inject context. Invalid
   input and storage errors are advisory native hook failures; they neither stop
   the turn nor claim successful recovery.
5. Automatic registration preserves file-based hooks, does not write configuration
   files, and leaves explicit CLI hooks untouched with a visible registration notice.
6. Without prior `/hooks` approval, the registered hook is trusted and runs; a user
   `enabled = false` state for its key still disables it. Opting out registers no
   hook and no trust state.
