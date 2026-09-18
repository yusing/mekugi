# Agent guidance

## REQ-GUIDE-001 — Agent guidance

One shared guidance source owns durable tool and CTP/2 representation rules and
contains one model-specific workflow slot. Astra requests select the concise Astra
workflow; every other or missing model selects the default workflow. Selection occurs
for each eligible request, including model switches and inherited marked prompts,
independently of native versus CTP/2 transport. Model selection changes workflow
wording and examples, not shared syntax, atomicity, recovery, or journal routing.
Model-specific phrasing does not alter the shared reference contracts. Guidance includes effective
use of tool capabilities: batching related edits against immutable baselines, reusing verified
targets, selecting suitable mutation forms, and leaving formatting to the engine. It does not
prescribe general task autonomy, approval checkpoints, prose length, validation scope, or a ban
on inspecting changed files. Those policies remain with the host and task instructions.
Each file in the [interface contract index](index.md) owns one normative engine or
router contract. Model-visible tool descriptions contain only concise call-local
behavior and request-specific schemas; submission, batching, and continuation guidance
stay in the shared guidance rather than being repeated in descriptions. Private command
descriptions are never used as prompt text. Native transport omits CTP/2 guidance;
CTP/2 includes it and transforms only the eligible strings defined by `REQ-CTP-001`.

Before provider forwarding, the router strips blocks enclosed by the exact HTML comments
`<!-- mekugi:omit -->` and `<!-- /mekugi:omit -->`, including both comments.
This applies to top-level instructions, system and developer message text, and the
`<INSTRUCTIONS>` body of Codex user-role instruction context (including AGENTS.md).
Ordinary user messages, assistant history, tool results, and non-text parts remain intact.
Each text part is processed independently, including fenced text; nested and multiple
complete blocks are supported. Unmatched markers and bytes outside complete blocks are
preserved. This request-local filtering also applies to prewarm and execution-free requests,
before model-guidance rewriting or provider encoding, without changing instruction files.

For each eligible turn carrying a non-null Responses `instructions` string, the router refreshes
one current marked mekugi section or replaces the pinned stock Codex file-editing section and its
displaced rg and exec-command lines. The GPT-6 Astra stock template has no file-editing section:
the router recognizes its pinned introduction and work-rules heading, replaces the pinned rg line
immediately after the heading and blank separator with central guidance, and removes the pinned
exec-command line. The active Astra prompt may instead have no legacy exec-command line and one
pinned transport-independent shell-safety line after the search line; that safety line is preserved.
The search and execution anchors must be unique, and an old file-editing section must be absent.
For stock, marked, and configured custom prompts, the router removes complete pinned
checklist-tool fragments before refreshing guidance, independently of Codex's launch-time
filtering. A checklist introduction does not establish ownership of its surrounding section.
Unknown wording, same-line suffixes, indented continuations, headings, fenced examples,
and adjacent caller policy remain intact. Removed fragments retain their line boundaries so a later refresh
cannot expose a new match. Ordinary planning and edit planning remain intact. The router also rewrites pinned conflicting
progress-channel, initial-update, skill-announcement, approval-rejection delivery, 60-second wait,
Code Mode batching, unrestricted parallelization, Plan mode's repeated-question prompts, and
Default mode's `request_user_input` prompts when that request's tool contract makes it Plan-only
outside the owned section. Progress uses journal mutations. Known reads and searches batch in a
shell script. Ready work batches while dependent operations remain sequential; neither hpatch nor
mutations generally have a blanket isolation rule. Inherited pinned batching lines refresh to the
same dependency-only rule.
Plan mode asks only the questions needed for a decision-complete plan, while Default mode does
not call `request_user_input` when the request's tool description restricts it to Plan mode;
Default-enabled host guidance is preserved. The rewrite applies to every developer-message
instruction, including collaboration-mode instructions delivered separately from the main model
instructions. Unrelated instructions, including authorization, validation,
and shell-safety rules, are preserved. These rewrites cover the GPT-6 Astra and shared GPT-5.6
Sol/Terra/Luna templates and the active Codex prompt, including its “To reduce round trips”
batching prefix and line-wrapped status-reply instruction. At startup,
the router reads `$CODEX_HOME/config.toml`, falling back to `~/.codex/config.toml`, only to
snapshot whether the top-level `model_instructions_file` key is set. A configured custom prompt
without recognized stock or marked guidance receives the central guidance by append; without that setting,
the request fails before upstream forwarding as an unsupported upstream instruction change.
Missing and null `instructions` values remain unchanged. This request-local behavior covers
session start, post-compaction, subagent start, and subagent post-compaction instruction delivery;
an inherited side conversation refreshes the marked section already in its prompt.
Neither installation workflows nor the router create, change, or remove instruction
files.

The capture evidence in `REQ-METRICS-001` records the actual instruction carrier, matched rewrite
strategy, selected model workflow, and whether a custom instruction file was configured. Prompt
shape matching tries both stock shapes independently of workflow selection, including an
Astra-shaped override sent to a non-Astra model. Evidence describes the rewrite decision, not
proof that the model followed the guidance or that a later forwarding step succeeded.
The WebSocket transport must not elide rewritten inherited instructions against a provider
prefix containing their old values; its cache replacement contract is in `REQ-ROUTER-001`.

The recovery template adjacent to the central source owns dynamic target-only recovery prose.
The shared Rejected-script recovery section explains the two payload forms in
[REQ-CORRECT-001](correct.md): current command handles for wholly row-stale failures,
or ordinary mutations against retained-script text for other and mixed corrections.
Dynamic diagnostics supply current handles or bounded script-row context. Re-rejection
invalidates prior handles and advances the retained baseline; invalid correction payloads
leave it unchanged. Neither form requires re-emitting unrelated prepared edits.

Both model variants point to shared references rather than repeat submission syntax.
The shared guidance must make these choices directly available in native and CTP modes:

1. **Execution:** one multiline script for ready commands sharing execution options; shell
   background jobs with explicit waits and failure preservation for slower independent work;
   explicit sequential batches only for separate execution contexts; and host-provided
   continuation actions under
   [REQ-SHELL-001](shell.md). Reusable source belongs in ordinary script files.
   A failed execution is not a rollback.
2. **Acquisition:** reuse known literals, verified rows, and confirmed mappings first.
   Otherwise select fixed-string hgrep, bounded hcat, structural inspection, or semantic
   lookup with a current line or verified row. Explain automatic worker display budgeting
   and use reader controls to focus the requested context.
   Continue cursors or captured output without replaying producers;
   distinguish reader omissions from outer truncation. Explain preview and truncation limits before using partial source as an
   exact target. Structural edits acquire semantic references across affected callers
   and tests before grouping dependent edits; filename filters are not coverage evidence.
   Incomplete or skipped references remain explicit until resolved.
   Reader contracts remain in [read.md](read.md), [grep.md](grep.md),
   [inspect.md](inspect.md), and [symbol.md](symbol.md).
3. **Editing:** group ready related edits against immutable baselines; split dependent work
   only when validation or missing facts must determine the next edit. Prefer insertions and
   targeted replacements, leaving language-aware formatting to the engine. Run
   `hpatch` as a standalone shell command under [REQ-SCRIPT-001](script.md), with
   ordinary argument, stdin, expansion, and redirection semantics. Guidance
   distinguishes atomic validation from potentially partial application and never
   treats cancellation as proof of rollback or process termination.
4. **Values and boundaries:** keep quoted values and heredoc syntax, newline ownership,
   empty-value deletion, and advisory interpretation together in the shared HPATCH/2 reference.
   [REQ-SCRIPT-001](script.md), [REQ-EDIT-001](edit.md), and
   [REQ-OUTPUT-001](output.md) own the behavior.
5. **Recovery:** use `hpatch --recover HANDLE` and choose a correction form in one
   shared section, using the explicitly named rejected baseline rather than
   workspace rows. Follow [REQ-CORRECT-001](correct.md) for immutable records,
   atomic reevaluation, and invalid corrections. Native yielded work uses Codex's
   continuation tools; failure or an unknown outcome does not authorize replay.

Shared journal guidance prefers mutations on a useful ordinary call whose schema carries
`journal`, or the reserved `journal` command inside `functions.shell`, over a standalone
`functions.journal` round trip. It includes Bash/POSIX `journal add 'text'`, the full shell `journal` CRUD/list/batch/finish
syntax, and Code Mode `await journal({op: "add", text: "text"})`, alongside the optional field
for eligible structured tools. `functions.journal` remains the fallback for listing or finishing
when no current call or shell command can carry the operation. Both workflows receive the shared
syntax. The default workflow supplies the longer shell-execution tutorial; Astra points to the
shared reference.

The shared source supplies complete call syntax because it is injected into other workspaces;
it must not require the model to open this repository's specifications. Those specifications
own acceptance criteria, not additional prompt instructions.

The persistent reference uses compact syntax/decision tables and gives each invariant one owner.
It keeps ambiguity-resolving examples but omits tutorials, duplicate lifecycle prose, and
implementation detail that cannot change the model's next action. Actionable diagnostics may add
failure-specific correction syntax, but ordinary and advanced operations remain discoverable
without a preliminary help call.

Rendered guidance is bounded with the pinned GPT-5 tokenizer: native Astra is at most 3,900 tokens,
native default at most 4,050, CTP/2 Astra at most 4,400, and CTP/2 default at most 4,550.
Tests enforce these ceilings together with the required semantics so prose cannot silently regrow.

Acceptance:

1. A model can choose and encode every HPATCH/2 operation from the persistent guidance.
2. The forwarded prompt contains the selected central guidance exactly once and omits the pinned
   stock apply_patch, rg, and exec_command instructions. Native omits the CTP/2 section; CTP/2 retains
   it. Both the GPT-5 editing-section template and GPT-6 Astra work-rules template are supported.
   The workflow follows the request model, not the stock prompt shape or the proxy's first model.
   Switching models refreshes the existing marked section without retaining the other workflow.
3. A marked prompt retains unrelated content before and after the owned section and refreshes
   idempotently. Checklist removal preserves caller authorization and safety text even in
   the same section or list as a tool instruction, including multipart developer content;
   it matches complete pinned fragments rather than open-ended prefixes or section heuristics.
   A configured custom prompt without a recognized section retains unrelated content before
   the append. Pinned conflicting tool and progress fragments are rewritten in both paths,
including fragments inherited from earlier rewrites. Compatibility covers all four
cached model IDs, both instruction carriers, and both model protocols.
4. Missing and null request instructions remain byte-equivalent. An unconfigured, unrecognized
   non-null instruction string fails before forwarding. CTP/2 never creates or encodes its selected
   instruction carrier, and `ctp1` fails before router startup.
5. Dynamic rejected-script references and recovery prose appear only with actionable context.
6. Recovery guidance distinguishes target-only shortcuts from script-text edits, preserves
   unrelated prepared edits, and explicitly invalidates prior handles after re-rejection.
   It does not direct non-target or mixed failures to re-emit the complete script.
7. A routed success can be followed by another hpatch call using an exact row from its report
   without an intervening hcat; a saved pre-edit row still rejects as stale.
8. Both rendered model workflows include the shared journal, framing, boundary, recovery,
   continuation, lifetime, and reader contracts exactly once, including combined output
   budgeting and complete semantic reference acquisition. Guidance uses existing limits
   and supported continuation, explains the worker's aggregate display budget and retained
   output files, and does not imply a universal reader cursor. Superseded requirements for verified-only semantic queries
   or full-script recovery are absent.

9. Every rendered model/transport variant stays within its token ceiling while retaining the
   persistent syntax and behavioral contracts above. Compression does not move required guidance
   behind another tool call or duplicate it in call-local descriptions.
