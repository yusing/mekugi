# HPATCH/2 script grammar

## REQ-SCRIPT-001 — HPATCH/2 script grammar

Outside a multiline value body, blank lines are ignored and every other physical line begins exactly
one command:

```text
in PATH
new PATH
mv PATH
rm
type TARGET VALUE
add DESTINATION VALUE
type VALUE
```

The final form is new-file initialization and is valid only under `REQ-FILE-001`.

Targets are:

```text
ROW                         complete logical line
ROW..ROW                    inclusive complete-line range
ROW "TEXT" [COUNT]          anchored exact literal occurrence(s)
"TEXT" [COUNT]              whole-baseline exact literal occurrence(s)

ROW   := LINE:HASH
LINE  := positive one-based decimal logical line
HASH  := exactly four lowercase hexadecimal digits
COUNT := positive decimal integer; default 1
```

An add destination is a single `ROW`, an anchored or unanchored text target, or the literal
`EOF`. `add` does not accept a range. `EOF` is a destination sentinel rather than a target
and contributes no target metric.

No whitespace is permitted inside `ROW..ROW`. A line target owns the complete logical
line, including its terminator when one exists. A range owns all
complete logical lines between its endpoints, inclusively.

A text target either verifies its anchor row and starts at that row's column 1 or, without a
row, starts at byte zero. It searches exact literal content forward through EOF. `TEXT` is
nonempty. Its quoted source remains on one physical command line, but JSON-escaped LF (`\n`
or an equivalent `\u000A` escape) decodes into the exact target literal and may make one match
span logical lines or include a trailing LF. Literal horizontal tab is also accepted. Raw
physical newlines, CR in every representation, and every other C0 control are forbidden.
Matching is left-to-right and resumes after each complete match. The target contains the first
`COUNT` non-overlapping matches and rejects if fewer exist.

`VALUE` is a JSON-compatible quoted string or a heredoc.
Inline strings decode JSON escapes and Unicode escapes and additionally accept literal
horizontal tabs. Quotes, backslashes, line terminators, NUL, and other C0 controls remain
escaped.

A heredoc uses a caller-chosen delimiter, optionally single- or double-quoted:

```text
type 12:a1b2..15:c3d4 <<'END'
replacement
text
END
```

The closing line must equal the delimiter after quote removal. `<<-END` strips leading
tabs from body and closing lines, as in shell heredocs; it does not remove the final newline.
Spaces are not stripped. Delimiters are nonempty shell words parsed by `mvdan/sh`;
quoted parts and backslash quoting may be combined. Quote spaces and shell metacharacters.
Whitespace between `<<` (or `<<-`) and the delimiter is accepted.

Bodies are literal UTF-8: no interpolation or escape processing occurs, regardless of
delimiter quoting. Every body line retains its physical LF/CRLF terminator. Bars and
command-shaped lines are ordinary data. Choose a different delimiter when the body
contains the closing line. There are no reserved PATCH/TEXT modes or newline-chomping
suffixes. Use a quoted string when the value must omit its final newline.

The header, body, and delimiter form one command attributed to the header. Missing closes,
invalid UTF-8, and bodies over 1 MiB reject the entire script before mutation. The limit
applies to decoded bytes after optional tab stripping. Existing transport limits still apply.
An unterminated frame owns the remaining input, so payload-shaped commands are not executed.
The model-facing context-free grammar admits candidate closing lines; the shared parser
checks that the closing delimiter matches the header before any effects.

Whole-line replacement still applies the terminator-preservation rule in `REQ-EDIT-001`.
Literal targets own only their exact matched bytes; heredocs do not consume adjacent
baseline whitespace.

The grammar is unambiguous by operand shape. For example:

```text
type 12:a1b2 "line replacement"
add 37:8c2f "// parseCommand parses one physical script line.\n"
type 12:a1b2 "needle" "replacement"
type 12:a1b2 "needle" 3 "replacement"
type "known current text" "replacement"
add EOF <<PATCH
appended text
PATCH
```

Paths are nonempty and consume the remainder of their command line. Root-scoped library application
through `Apply` or `ApplyForHost` resolves relative paths from cwd; absolute paths must remain beneath
the canonical root, and lexical or symlink escapes fail. Host translation through
`TranslateForHostAt` instead uses an optional canonical metadata directory without filesystem
confinement. With a directory, relative operands resolve from it; without one, relative operands
reject and absolute operands remain valid. Router process cwd is never an implicit base. Emitted
patch paths retain cleaned host identities for Codex to authorize.
Trailing operands, malformed rows, forbidden controls, missing values, and unknown
commands are invalid.

Acceptance:

1. Every engine command is one of the six public edit commands. Routed mixed scripts additionally accept the shell forms below.
2. Line, range, anchored text, and unanchored text targets parse without a separate selection command, and inline
   replacement values remain distinguishable from a text target's quoted literal.
3. Anchored and unanchored text targets accept JSON-escaped LF and exact multiline or
   trailing-LF matches while raw physical newlines, CR, empty literals, and other forbidden
   controls reject.
4. JSON-compatible values and heredocs reproduce their decoded payloads without parsing
   body lines as commands. Caller-chosen and quoted delimiters, tab-stripping heredocs,
   literal bars, command-shaped payload, and LF/CRLF terminators work for every target shape.
   A missing or mismatched closing delimiter rejects atomically.
5. Invalid rows, ranges, counts, strings, heredocs, operands, and commands fail before
   filesystem mutation, patch output, or final-state reporting.
6. File and mutation commands may be interleaved while all targets retain the immutable
   baseline meaning defined by `REQ-SELECT-001`.
7. For root-scoped evaluation with root `/workspace` and cwd `bin/worktree`, path `main.go` denotes `/workspace/bin/worktree/main.go` and translates as `bin/worktree/main.go`.

### Shell-in-script

The routed `hpatch` tool additionally accepts `shell COMMAND`: every byte after
the first `shell ` through the end of the physical line is one raw program.
Quotes, pipes, redirections, and shell operators require no HPATCH escaping.
The physical LF/CRLF separator is not part of the single-line source. A trailing
backslash does not consume the next HPATCH line. Empty or whitespace-only source
is a successful no-op and starts no host process.
The literal `<<` is forbidden anywhere in single-line source, including
inside quotes, comments, here-strings, or arithmetic. Use the block form for such
source and for programs spanning physical lines; ordinary single-`<` redirection
remains allowed.

The multiline form is `shell` followed by a heredoc, using the same delimiter and
literal-body rules as edit values. The program may be empty or whitespace-only.
Missing closes reject before execution, never falling back to single-line shell source.
Both shell forms inside HPATCH values remain value data. Existing whole-input and
1 MiB body limits apply to both forms.

#### Segments and validation

Both shell forms may appear before, between, and after edit commands, including in a
shell-only input. Each contiguous nonblank edit segment is evaluated and validated
as a whole against its own immutable baseline, then submitted as one host patch
when changes exist. This is atomic validation, not a guarantee of atomic host application across files;
[REQ-OUTPUT-001](output.md) owns application guarantees.

Each shell command is one independent program using `REQ-SHELL-001` interpreter
selectors and execution directives. Shell batches and interactive programs use
separate shell calls. Params, shell variables, cwd changes, active file selection,
and pending edits do not inherit across segments. Each edit segment therefore
starts with `in` or `new`; its path base remains the request's canonical workspace
metadata directory, not a preceding shell's cwd.

Mixed scripts require Code Mode. The router validates all frames, edit syntax,
and shell headers before emitting any execution carrier. Workspace-dependent
target resolution, language checks, and patch translation occur only when an
edit segment is reached, after preceding host sessions finish. Codex authorizes
and applies each resulting patch through its normal patch tool. Neither the
router nor the translation worker executes shell on behalf of Codex or applies
the translated workspace edits directly.

#### Failure and application outcomes

No later segment starts after the first rejected edit, nonzero terminal shell
exit, translation failure, host refusal/error, or cancellation. A yielded session
is awaited, never restarted. Completed effects remain applied.

Shell failure means the program's terminal exit status, not every individual
command's status. The carrier does not implicitly enable `errexit` or `pipefail`.
For example, `shell false; true` succeeds overall. Authors use `&&`, explicit
status checks, or interpreter options when an earlier failure must determine the
program's result. A failed program may already have made changes.

Results must distinguish these outcomes:

- Preflight rejection: no segment ran.
- Edit evaluation, validation, or translation rejection before application:
  that edit segment changed no workspace files.
- Shell nonzero exit: the program failed, but its earlier side effects remain.
- Validated edit no-op: the segment completed without submitting a patch; its
  report describes no changes rather than claiming a write occurred.
- Successful host application: that edit segment completed, and its report may
  be published as a success report.
- Host application error or interruption after submission: the edit may be
  partially or fully applied. Without host confirmation, its outcome is unknown,
  not a validation rejection or a successful completion.

No failure result implies rollback. In particular, a failed or interrupted host
application must not claim the edit segment left the workspace unchanged.

#### Progress and cancellation

The carrier persists checkpoints privately without emitting lifecycle notifications
into model context. Persist segment start before its first host operation, entry into
host patch application before submitting the patch, and completion only after confirmed
success for that segment. Persist each returned native session handle before awaiting
its continuation. The host retains the ordered result and sequence summary. For a fully
successful call, the model-facing view contains only the edit reports and native shell
results, in order, with no checkpoint messages, repeated segment metadata, resume handle,
expiry, or sequence wrapper.

When an actual host result yields or lacks a complete mixed-script summary, the router
projects compact recovery information onto that result using validated call provenance
and the private retained state. Wait results inherit mixed-script provenance only from
visible originating calls and host cell IDs. Projection never executes or resumes work.
It preserves the execution evidence and continuation choice; an outer cell still owns its waits.

Recovery identifies the handle and expiry, one-based segment, original physical line,
kind, phase, last confirmed status, completed prefix, and known native sessions.
Only the outstanding result receives this annotation; a complete summary needs none.
Keep recovery separate from command output so truncation does not hide it. An unavailable,
expired, or invalid retained record is reported as unavailable, never reconstructed as
successful work or treated as proof that a process stopped.

Before exposing the compact success view, the router persists a digest of that exact
view, bound to the originating mixed call's durable replay record. This is output
provenance, not another execution or permission to resume. Replayed compact output must
match its receipt; missing, altered, failed, or incomplete results are never inferred to
be successful. Receipt storage failure leaves the full host result unchanged. Receipts
follow the call record's retention policy, not the private continuation's lifetime.

On ordinary completion or a catchable failure, retain ordered segment results.
Completed edits include their reports; shell results preserve terminal native
fields and ordered output. The final `sequence` summary records total, started,
and unstarted segment counts and `stopped_reason`. Catchable host errors publish
the completed prefix and current partial output, including any outstanding
native session, before propagating.

Hard Code Mode termination may skip JavaScript cleanup and the final summary.
The router recovers the last persisted checkpoint when projecting the host's termination
or incomplete-result output, even if JavaScript cleanup did not run. A start or application
checkpoint without subsequent confirmation means the operation is unresolved; absence of
a final summary is not evidence of success, rollback, or process termination.

Cancelling a continuation wait is not proof that its underlying process stopped.
Cancellation remains host-owned: use supported host cancellation mechanisms, and
claim termination only when confirmed. If a yielded session may still be running,
preserve its known handle for inspection or termination instead of restarting it.
If interruption occurs before a handle or terminal result is available, report
the activity as unresolved rather than inventing a handle or claiming it stopped.
The caller must resolve potentially live work before retrying or starting
overlapping work.

#### Retained continuation

A successfully preflighted mixed script receives a short word handle, such as
`maple`, with a decimal suffix when needed. The original script, prepared segments, completed
results, current segment, native-operation journal, and resume position remain in
the existing private thread storage. Retention lasts one hour from creation,
ends on router shutdown, and is not renewed by reads or resumes. Handles are
thread- and workspace-scoped, not durable replay records. Invalid, expired,
unavailable, or cross-workspace handles reject before execution.

Use the routed `hpatch` tool:

```text
resume HANDLE
resume HANDLE retry
resume HANDLE repair
resume HANDLE accept
```

A plain resume continues pending work without rerunning completed segments or
confirmed native operations within the interrupted segment. It may await an
already-known native session. Remaining edit targets are resolved against current
files; interrupted pre-application translation is finished before being discarded
and translated afresh. Cached translation is never used to apply a patch after
a resume. A previously confirmed application can finish its report without
reapplying the patch.

Before any resume, resolve the previous Code Mode cell: it must have finished or
been terminated. Cancelling that cell is not confirmation that its native work
stopped. A pending operation without a returned result, a nonzero shell exit,
or uncertain host application requires inspection and reconciliation.
`retry` explicitly confirms potentially live work has ended and uncertain effects
have been inspected; it retries only the current segment against current state.
`accept` confirms the segment's intended state has been established externally
and all its native work is resolved; it records `reconciled`, not a successful
application report, and advances to the unchanged suffix. Neither action cancels
or restarts an existing session on the agent's behalf.

`retry` may be followed by a newline and one replacement segment of the same
kind. An edit replacement includes its own `in` or `new`; a shell replacement uses
the ordinary `shell` syntax. The replacement is retained if it fails again.
Completed and unstarted segments remain unchanged. Never ask the agent to resend
the complete original script or regenerate its unchanged suffix.

`repair` requires one workspace edit segment after the header. It carries the same
inspection and live-work reconciliation requirements as `retry`. The carrier inserts
the repair before the failed segment, applies it through the normal host patch tool,
then retries that segment and continues its retained suffix in the same invocation.
A previously supplied replacement for the failed segment is preserved. Syntax is
validated before effects; repair targets are validated when the repair runs.

The repair is retained under the existing handle and expiry, with no new model call
between repair, retry, and suffix execution. Its checkpoints and result carry
`repair: true`. Sequence positions and counts include inserted repairs; completed
prefix positions remain unchanged. Original physical-line references are preserved.
A failed or interrupted repair stops before retrying the original segment and can
itself be resumed, retried, replaced, or reconciled through the same handle. Completed
repairs are not replayed after interruption. No repair is inferred from unrelated edits.

Each carrier uses one argument-free `shell` control channel for checkpoints and
edit translation. The helper discovers storage through inherited `CODEX_THREAD_ID`
and binds a retained handle from a bounded stdin frame; no private flag, path,
connection detail, or inline environment assignment appears in command arguments.
Replies use bounded, acknowledged chunks so host output truncation cannot silently
lose translation data. Checkpoints send only changed progress fields, and translation
results already held by the control process are referenced rather than copied back
through terminal input. Edit segments share one carrier implementation instead of
retaining generated per-segment programs. Actual shell commands retain their ordinary
displays. A host may still display the control-channel call and compact stdin frames,
but it does not receive repeated full checkpoint snapshots. Closing the channel never
cancels a workspace shell process. Abandoned unbound channels expire after one minute;
bound channels expire with their retained handle.

Checkpoint persistence is independent of Code Mode cleanup. A checkpoint is saved
before each native operation and after its result, including known session handles.
A hard interruption during checkpoint publication can leave the operation unresolved;
recovery annotations describe retained facts, never prove rollback.
Revision checks stop stale carriers before further operations, but are not a
substitute for resolving live work before resuming. Private retained state has a
32 MiB limit; storage failure stops subsequent execution without undoing effects.

Successfully preflighted mixed scripts do not enter rejected-script recovery or
publish cross-invocation replacement aliases. A preflight failure before carrier
retention has no execution effects and may use script-text recovery under
[REQ-CORRECT-001](correct.md); once corrected preflight succeeds, the retained
continuation interface owns all remaining work.

Result rows refer to the completion of their own edit segment and may be changed
by a later segment.

`ValidateScriptSyntax` validates engine syntax without filesystem access or
evaluation. Library apply/translation entry points remain edit-only and reject
shell commands without mutation; the routed carrier owns the mixed workflow.

Additional acceptance:

1. Legacy edit-only grammar and engine behavior remain unchanged. Malformed later
   edit syntax or shell headers prevent even an otherwise valid prefix from running.
2. Shell bodies containing HPATCH-looking source are byte-preserved; markers in
   heredoc edit values never become execution boundaries.
3. Shell-created or modified files become the actual baseline of the following
   edit segment. No patch is translated against a pre-shell snapshot.
4. Edit rejection before application and shell nonzero exits leave completed
   segments intact and later segments unstarted. A failed shell's own side effects
   remain. Yielded host sessions finish before the next segment begins. Test both
   a terminal nonzero program and an intermediate command failure followed by
   terminal success; only the former stops the sequence.
5. Truncated or malformed private translation output never reaches patch
   application. Replay restores the original mixed input and existing carrier,
   without retranslation, reexecution, or automatic recovery of completed effects.
6. Native-only clients reject mixed scripts before effects while retaining their
   ordinary HPATCH interface.
7. Single-line commands preserve quotes, operators, and whitespace after `shell `,
   stop at the physical line boundary, and compose with blocks and edit segments.
   The reserved exact block opener cannot fall back to inline execution when
   its close is missing; any other inline `<<` rejects before effects, including
   occurrences inside quotes.
8. A host patch that changes one file and then fails on another stops later
   segments without reporting completion or claiming the segment was unchanged.
   Interruption after patch submission but before confirmation is likewise an
   unknown application outcome. Exercise this through the native host boundary,
   not only a mocked validation rejection.
9. Hard termination of a real Code Mode cell after a completed segment preserves
   privately persisted progress even when `finally` does not run. Test termination while
   awaiting an already-yielded shell session: its known handle remains available,
   later segments do not start, and stopping the wait is never reported as proof
   of process termination. Test interruption around host patch submission too;
   a missing completion checkpoint must not turn uncertain effects into a safe
   automatic retry.
10. Successful mixed calls emit zero lifecycle notifications and no model-visible
    success wrapper. Their edit report and native shell payloads match separate calls;
    replay, output-only history, and waits preserve the compact view without markers.
    Failures, reconciliation, and missing confirmation retain full recovery evidence.
    Verbose or truncated
    output does not hide recovery information: an incomplete result receives a compact
    annotation from retained state, including known session handles. Complete summaries
    receive no additional annotation. A final summary is required for ordinary completion
    and catchable failures, but not fabricated after hard termination. Repeated yields,
    waits, resume calls, output-only replay, and unavailable storage preserve this boundary.
11. A shell failure between edit segments can be repaired and resumed by handle,
    without resending the unchanged suffix or replaying completed effects. Files
    changed between failure and resume receive fresh target validation.
12. Invalid or unavailable resume handles execute nothing. Explicit retry can
    replace only the failed segment. After mixed preflight succeeds, rejected-script
    recovery directs the work to retained continuation without a false success claim
    or fallback to an older rejected script.
