# Agent guidance

## Required outcomes

Continuity uses selected current sources rather than summaries as source substitutes.
Retain context-usage notices and known-source preload for delegates. Codex resets
context without a generated summary; Claude retains its native compaction behavior.

Prompt projection, source-selection syntax, reload orchestration, snapshot fields and
exact notice thresholds/reset rules below are current design.

## Purpose and scope

Assemble contributed frontend guidance and project explicit omissions and selected-skill identities without changing caller authority.

## Behavior

Mekugi preserves the caller's base `instructions` value and every system or developer message,
except for complete explicit omission blocks and the
marked Mekugi workflow section appended to base instructions. Its owned environment
supplement is projected separately after static instructions as described below.
Independent-thread role projection additionally consumes Thread Control's admitted
content provenance and detached logical caller. It adapts only authenticated role
parts to the replacement MCP interface and selected workspace. Effective mode
parts and caller-authored lookalikes remain unchanged; missing or conflicting
provenance never authorizes a text-based replacement.
It does not detect stock prompt shapes, replace editing sections, select model-specific workflows,
inspect `model_instructions_file`, or create or modify caller instruction files. It does not restore the
retired shell carrier, HPATCH, hash-target editing, or CTP instructions.

Default model instructions belong to the native runtime's bundled model catalog.
The default catalog uses its complete model-message set even when cached or remote discovery
supplies capability metadata. Existing model matching selects the bundled template; unknown models
use the native generic prompt. Explicit provider/static catalogs and explicit base instructions,
including empty instructions, remain authoritative. Personality configuration still applies.
Native templates omit forced commentary cadence, mandatory skill announcements and assumptions
that compaction supplies a summary. Wait guidance uses completion notifications or interruptible
waits. These policies are source text, not request-time phrase substitutions.

Inherited base instructions on resume and full-history forks retain their original text.
Stock CLI passthrough also retains stock prose. Neither is silently rewritten to match the
new native templates. Native context assembly does not advertise uninstalled plugins.
When a user explicitly requests a missing plugin, the existing discovery tool lists eligible
candidates before installation is requested. Endpoint candidates retain their original ID
validation, metadata hydration and install-source attribution. Historical advertisement
markers remain recognized as context, not user authorization. Caller-authored plugin policy
is preserved without request-time matching.
Planning guidance and the stock plan tool remain host-owned. Invocation-local Codex flags
omit collaboration-mode instructions.

Each executable helper owns its capability description and command-keyed workflow
guidance. Descriptions contain capabilities, syntax, options and output semantics;
workflow guidance explains when and how to use the helper during a task. Absent
command contributions contribute neither description nor workflow guidance. The
module renders both from the validated invocation catalog, without a duplicate
checked-in catalog. The application authenticates and publishes both sections
before exposing its commands.

The same catalog supplies native-shell guidance for Claude delegates. Command
descriptions, helper workflows and child reply policy are shared and backend-neutral;
Codex JavaScript examples, exec-output visibility and Codex-specific shell-selection
guidance appear only in Codex requests.

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
Only macOS hosts include execution guidance to select Bash explicitly unless
the task requires another shell. This leaves Codex's account-shell detection and
user shell commands unchanged.
CLI help includes concrete invocation examples and consequential option interactions, including
inclusive file ranges and line limits within those ranges. A smaller window is incomplete
evidence, not coverage of the full requested range.
Default token budgets are 6000 for file reads (`mcat`), 8000 for retained-output continuation
(`mread`), and 4000 for symbol queries and structural outlines. Explicit token limits override
these defaults up to 15500; `mcat -n` retains its default token ceiling.
It preserves context reuse and batching of ready related edits without replacing the stock editor.
Scripts or substantial text expected to be reused go to one temporary file outside the
repository that later calls edit, invoke, reference or preload instead of resending the content.
Review handoffs include the requested scope and explicit IDs or same-agent inclusive ranges;
the recipient's own change listing cannot discover another agent's IDs. Historical edit evidence
does not assert the current workspace state.

The invocation's `child_reply_summary` feature conditionally contributes a rule
inside the existing marked workflow section. Enabled children author child-to-parent
directed messages and final answers long enough to benefit from structure with a concise, meaningful
`## Summary` in the same existing reply generation, followed by supporting sections
under headings the child chooses.
The summary states the actual result, findings or blocker and material uncertainty,
normally in one to three sentences. Short replies may remain plain, including substantive
replies with supporting details; they need no padding or duplicate summary.
Higher-priority exact-output requirements and root ordinary answers are unchanged.
There is no separate summary message, repair turn or extra model request.
Disabled invocations omit only this rule. Authenticated assembly applies the
current invocation value once to fresh/no-history children, forks, switches and
resumed requests, preserving caller text outside the owned workflow section.

Before provider forwarding, the router strips blocks enclosed by the exact HTML comments
`<!-- mekugi:omit -->` and `<!-- /mekugi:omit -->`, including both comments. This is an explicit
caller-authored filtering contract. It applies to top-level instructions,
system and developer message text, and the `<INSTRUCTIONS>` body of Codex user-role instruction
context, including AGENTS.md. Ordinary user messages, assistant history, tool results, and non-text
parts remain intact. Each text part is processed independently, including fenced text; nested and
multiple complete blocks are supported. Unmatched markers and bytes outside complete blocks are
preserved. Filtering also applies to prewarm and execution-free requests.

When `skills-mgr` is executable in the wrapped Codex PATH, the wrapper enforces the
invocation-local Codex setting `skills.include_instructions=false`, so the stock skill catalog is not
duplicated alongside the skills-mgr catalog. The router replaces explicitly selected Codex skill
instructions with the compact identity `<skill name="…"/>` before provider forwarding. This
idempotent projection applies to ordinary, prewarm, execution-free, and replayed selected-skill
messages without modifying Codex configuration files. When `skills-mgr` is unavailable,
both stock catalog instructions and selected-skill messages remain unchanged.

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

## Environment snapshot

Agent Guidance supplements Codex's native environment context with a snapshot-reuse hint,
platform and OS identity/version, kernel release, the current user's home directory, and an explicitly observed
Git snapshot. The home directory comes from the platform's user-home environment and remains
explicitly unavailable when it cannot be resolved; it does not depend on workspace or Git discovery.
Compact labeled text omits duplicate native workspace paths and empty collection diagnostics.
Git repository membership is yes, no, or unavailable. Plain and SVN-only directories report no;
missing Git, corrupt metadata and inspection failures remain unavailable with diagnostics.
SVN facts remain owned by the existing project hook.
Linux identifies its distribution from the host OS release metadata; macOS includes its product
version. Unavailable OS identity remains explicit. Repository/worktree membership,
branch and HEAD (including detached and unborn states), locally known symbolic default branch,
bounded HEAD containment in local remote-tracking branches, effective `core.hooksPath`,
bounded status, five recent abbreviated commit IDs/subjects and, when local remote-tracking
branches exist, the count of further unpushed commits are included. Hooks configuration and the
status excludes file use the applicable Git configuration scopes; other facts remain
configuration-isolated. Git account identity, email and remote URLs are excluded. Unavailable facts and truncation are explicit; acquisition neither
fetches nor mutates a repository and optional failures do not block inference.
The supplement is a developer input after the leading static instructions and
before the first user or conversation item. It never splits tool/result history.
Refresh removes inherited copies from base instructions and developer text before
inserting the current snapshot once. Static contents, native conversation items
and tool/result history boundaries remain intact; ordinary continuations reuse
the confirmed provider prefix.

Acquisition is a stateless contribution to the
[frozen initial input prefix](../../apps/mekugi/internal/requestexecution/SPEC.md#initial-input-prefix),
against the admitted execution workspace before generating inference. That owner supplies
context, fork and reset lifetime; the environment producer has no inheritance or refresh
policy. Same-workspace inherited contributions retain their original observation time;
fresh contexts and changed workspaces acquire new facts. Prewarm acquires nothing.
Native cwd, execution shell, date/timezone, child role and permission context remain
authoritative. Claude retains its native environment supply without an additional snapshot.

Codex process construction establishes the invocation-local `MEKUGI_ENVIRONMENT_FACTS=1`
capability only when this supplement owner is installed, discarding inherited values. Native
hooks observe the app-server environment; tool-shell configuration clears this capability
after caller overrides without changing unrelated shell policy. Each Mekugi child establishes
its own capability. Standalone Codex launched from a tool shell keeps its original hook context.
The capability conveys information, never execution authorization.

## Context usage notices

Measured notices at 50/60/70/80/90% reset after compaction and are isolated by native session/thread.
The 90% milestone includes “You must compact now.” alongside measured usage, including when
usage skips past 90%. This instruction does not trigger automatic compaction.
Each fresh model-facing milestone also emits a user-only progress event, shown as a
terminal and browser transcript row rather than error feedback. Replaying an annotation emits no event.
Codex adds developer context after fresh tool results or text-only user input and replays at most five
boundary associations, preserving native content, identities, base instructions and provider prefixes.
Claude uses post-tool-batch context. Prewarm, approval, compaction, already-started successors and unknown usage add no notices.

## Compaction context

Codex uses its native token-budget reset for manual and automatic compaction.
Reset makes no summary-generation request and does not inject a compaction prompt,
handover, notes, or recovery artifact. Native thresholds remain host-owned. Mekugi
leaves the history/notes extension disabled, disables automatic `notes.thread_hint`
on configured notes servers while preserving other tools and restrictions, and
excludes client tool outputs from cross-reset retention. Self-directed checkpoints
use Thread Control rather than the native direct-reset tool. Claude compaction is
unchanged.

An agent can select optional reload sources in `compact` or `wait_agent` with
`compact:true`, using a structured JSON selection in the initiating call. The
selection stays with the checkpoint. Self-checkpoints may read it before reset
for cost admission; accepted resets acquire it after reset before provider inference.
There is no separate selection request or
response-schema change. Attached skills refresh from current sources after a boundary;
other context without a selection is recovered through ordinary model-directed reads.
Missing recovery state requires no bootstrap or completion gate.

An optional temporary JSON artifact can hold reusable task state. It is a source
selected by the agent, never an automatic whole-artifact reload. Select only fields
needed for the next operation, using focused line ranges or ordinary JSON extraction;
remove obsolete facts instead of accumulating task history.

Selections use the [source-reference syntax](#source-reference-syntax) below.
Native skill identity comes from the selected runtime's native catalog, not a
caller-supplied skill path. Catalog resolution supplies metadata only; source
bytes still use the receiving reader's permissions. Ambiguous or unavailable
native identities retain explicit diagnostics. Historical source records remain
readable without admitting the former object syntax for new selections.
Captured-change references read the admitted caller namespace. Unavailable IDs and
incomplete chains retain available output and explicit diagnostics. Selection never
performs an automatic inventory. An empty selection is valid. There is no fixed
source-count limit; oversized captures fail explicitly and require smaller selections.
These storage bounds do not establish a complete-request token-fit guarantee.

Fresh delegate [context preload](../threadcontrol/SPEC.md#prepared-children-and-guidance)
uses the same selection and retained-result delivery. Reads use the continuing
thread's or selected child's workspace, permissions and approvals. Codex supplies
`AGENTS.md`; automatic acquisition excludes it at every directory level, including
explicit selections and previously captured results. Select needed implementations,
callers, tests, fixtures and guidance for the next unfinished operation.

File selections use `mcat` with its normal range, output and recovery bounds. An
incomplete file read reports its failure and, when available, an `mread` continuation;
its omitted rows are not included in the preload. Skill and change selections retain
their own readers.

Successfully selected file and skill text also resolves nested references using
the same grammar, including JSON string values.
Relative references resolve beside a native source, or in the admitted workspace
for managed skills. Only selected rows are scanned, never omitted rows or captured
diffs. The root counts as depth 1; references beyond depth 3 retain visible errors.
Host-owned `AGENTS.md` stays excluded at every depth. Nested results remain inside
the root's retained result with command identities and failure status, using the
same permissions, approvals, byte budget and immutable reload/preload snapshot.

Independent readers run concurrently as stock tools. A denied or failed source does
not prevent the others from completing. Prewarm and execution-free requests never
start reads. The authenticated reload frontend captures selected bodies through
retained output delivery; stock output windows carry only the reference. Provider
input expands it into a result per source and omits synthetic acquisition
calls and selection metadata. One introduction identifies the bodies as reusable
read context. Acquisition may yield internally; inference waits for terminal
acquisition and never receives accumulated partial results. Publication failures
remain explicit. Source rows participate in ordinary output deduplication.

Native history retains the complete calls and results. Completed reads survive
resume and fork without reacquisition; a branch created before acquisition reads
when it continues. Cancellation and approvals remain Codex-owned. No reader is
silently replayed. Historical Mekugi summary records remain readable for saved
sessions and summary dialogs, with their original authorization and source results;
new resets do not create them. Missing, corrupt or unauthorized historical references
fail before provider access.

Mekugi no longer discovers, loads or injects compaction prompts. Stock Codex may
still read a caller-configured `experimental_compact_prompt_file` during its own
startup, even though native reset does not use it.

### Source-reference syntax

Preload, reload, attachments and selected nested text share one reference language.
Each consumer retains its supported source kinds and acquisition policy.
Examples below escape markers so selecting this document does not acquire its
placeholders. Omit the leading backslash in actual references; the JSON example
uses standard Unicode escapes and can be copied unchanged.

| Source | Reference |
| --- | --- |
| File | `\@path/to/file` |
| Selected file lines | `\@path/to/file#L2-L4,L8-L10` |
| Skill | `\$skill-name` |
| Skill reference | `\$skill-name/references/file.md` |
| Composed captured changes | `\mchanges:blue1,blue3..blue7,blue10,birch2..5` |

Line ranges are inclusive and may also follow skill references. Quote paths
containing spaces after the marker, for example `\@"path with spaces"#L2-L4`.
In prose, terminal periods, commas, colons, exclamation marks and question marks
are punctuation, not part of bare references or line-range suffixes. Quote a file
path to retain its punctuation, for example `\@"file.md."`. Interior punctuation
is unchanged. Two dots in captured changes remain a range operator, including
an incomplete range that must report a failure.
Captured changes mix individual IDs and inclusive ranges, separated by commas
without spaces. A range may abbreviate a same-chain endpoint (`birch2..5`).
Keep IDs/ranges in one reference to request one composed diff; separate references
request separate results.

Preload/reload retain the JSON envelope `{"sources":[REFERENCE,...]}` with string
entries only, for example `{"sources":["\u0040file#L2-L4","\u0024skill-name","\u006dchanges:blue1,blue3..7,birch2"]}`.
There are no source-object alternatives. Bare paths, email addresses and escaped
markers remain literal. Explicit selections and references in JSON string values
are exact, including terminal punctuation; prose punctuation rules do not rewrite
them. Shared model guidance supplies this syntax once; tool
descriptions keep only operation-specific acquisition and delivery rules.

## Lifecycle, Codex connection, records, and failure

The module has no background resources. Historical summaries retain their contributed
record kind in the shared retained-session store; captured sources use output
recovery records. Its binding
registers early omission filtering, late selected-skill projection, frontend assembly, and marked-description and base-workflow replacement.
Omissions run before preparation, including compaction and execution-free requests.
Attachment matching precedes selected-skill compaction.
Projection errors stop forwarding. With zero guidance contributions, the module
still handles its instruction policies and descriptions of selected commands.
Removing the module removes its reload helper, projections and assembly; other
command execution remains available. Provider compatibility prose belongs to its backend consumers.

## Acceptance

1. Stock, custom, missing, null, top-level, and developer-carried base instructions are forwarded
   unchanged except for complete explicit omission blocks and the marked base-workflow section.
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
6. With `skills-mgr` in PATH, Codex launches disable stock skill-catalog instructions
   after caller overrides and selected skill wrappers reach the provider as `<skill name="…"/>`.
   Without `skills-mgr`, neither projection occurs.
7. Codex manual and automatic resets make no summary inference. Optional selected
   sources reach the first continuation without unrelated artifact contents or
   selection metadata; tool schemas and cache identity remain stable. Completed
   reads survive fresh resume and fork. Denied reads stay denied; failed publication
   exposes no reference. Historical summary recovery rejects missing or foreign records.
