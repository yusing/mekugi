# Output, final state, and failure behavior

## REQ-OUTPUT-001 — Output, final state, and failure behavior

The rendering-only `RenderFileWritePatch` entry point converts one literal truncating file
write to a complete `apply_patch` envelope. Unlike engine evaluation, it does not read the
filesystem, format or validate source, run hooks, or apply changes. It accepts only representable
paths and empty or LF-terminated UTF-8 text without CR or NUL, preserving every content byte.
The host's `Add File` action performs the unconditional write even when the file already exists.
Invalid paths or content return an error and no patch. This adapter is used by the shell carrier
specified in `REQ-SHELL-001`; it does not alter engine translation semantics below.

The shell command applies one complete edit through the engine under host execution authority.

Every engine evaluation entry point accepts one complete input and evaluates the entire script before an
external filesystem commit or translated patch is returned. Basic `Apply` returns only an error.
All apply and host entry points reject a nil context with `context is nil`, before evaluation
or finalization; host variants return a zero result without running hooks or publishing output.
`ApplyForHost`, `ApplyForHostRoot`, `ApplyForHostAt`, and `TranslateForHostAt` return `HostTranslation`, which carries
the rendered report, final state, diagnostics, patch summary, target aliases, and per-file
review diffs under `REQ-CHANGES-001`.
Before finalization, every changed Go file is parsed and canonically formatted;
valid formatter transformations do not reject the transaction. Parse failures from
all changed Go files are collected and attributed to the nearest relevant edit.
Supported Python, JavaScript, and TypeScript files receive syntax validation and
the same bounded diagnostic shape. Supported baseline-aware indentation corrections
run before validation; unsupported formats remain byte-exact or reject under the
documented indentation policy. Finalization performs no generic whitespace cleanup:
authored trailing spaces, spaces before tabs, interior blank lines, and blank lines
at EOF are preserved unless changed by the language-aware formatting or indentation
corrections above. This applies to replacements, insertions, appends, and content deletions through
both apply and translation.
An unchanged apply change set performs no filesystem operation and succeeds. An unchanged basic
translation returns an empty patch. A host variant additionally reports the already-satisfied
final state in `HostTranslation`.

Translation output contains file actions in deterministic first-touch order:

```text
*** Begin Patch
*** Update File: PATH
<unified diff hunks>
*** End Patch
```

Engine edits produce `Update File` hunks only. File creation, movement, and removal
belong to the outer shell, not the edit transaction. Translation is fully rendered
before it is returned.

After evaluation succeeds, host variants carry one fully rendered final-state report in
`HostTranslation`. Apply host variants return it only after commit succeeds; translation host
variants return it with the complete patch. The shell `hpatch` command emits the report after
application. Basic `Apply` does not return the report. Its line forms are:

```text
file PATH
last OP [PATH] COUNT ranges RANGE[, RANGE[, RANGE]] [ +N more]
files add=A update=U move=M delete=D
advisory COMMAND: EFFECT=COUNT ...
file PATH
refs COMMAND OP
LINE:HASH TEXT
```

When Go formatting changes the rendered edits, each affected surviving file also has a
`format (pre-format -> final)` section under that file's display context. Its source
coordinates refer to the edited content immediately before formatting, not the invocation baseline. A one-row replacement
emits `OLD_LINE:OLD_HASH -> LINE:HASH`. Larger replacements and insertions emit
`Formatted START-END` followed by every final `LINE:HASH TEXT` row in that block, without
preview shortening, using the report's printable indentation/control escaping. Deletions emit
`Formatted removed START-END before final line LINE`.
Unchanged runs shifted by formatting emit `shift OLD_START-OLD_END -> START-END (hashes unchanged)`.
Final row identities use the same logical-line and hash semantics as readers and targets.
These are formatter effects only, not a second report of the authored edits; the usual
post-format references remain authoritative. As with the rest of the report, translation
alone does not establish application.

The first line names the last command's file as display context, not active editing state.
The `last` line is `last none` when no mutation changed final content;
otherwise it names the last effective mutation operation, that file's path only when
different from the initial display context, the number of affected target spans,
and at most three verified immutable-baseline ranges. Extra ranges are summarized by `+N more`. `RANGE` is a half-open
`START_LINE:START_COLUMN-END_LINE:END_COLUMN` pair in one-based Unicode coordinates; a
complete-line range includes its final terminator when present. The `files` line counts
net original-to-final actions.

Host reports may insert one bounded advisory line per effective command between
the `files` summary and reference blocks:

```text
advisory COMMAND: deletes=1 removes-ending=1
```

These lines describe each command's authored splice against the immutable baseline,
before other commands or language formatting affect adjacent content. They are
inspection aids, not errors or claims about user intent, and never change bytes,
validation, aliases, or application. Failed or cancelled host results publish no
success report and therefore no boundary advisories.

The command number links each advisory to its `refs` block; paths and operations are
not repeated. Routine newline preservation and heredoc metadata are omitted.
An empty append is not a deletion.

Nonzero counts summarize effective spans in that command:
- `deletes`: an empty replacement removes target bytes;
- `removes-ending`: a replacement removes the target's final terminator without
  preserving or supplying one;
- `blank-before` / `blank-after`: a terminating side meets a blank, possibly
  space/tab-only line on the other side of the splice;
- `joins-left`: an EOF insertion continues a nonterminated baseline line without
  a leading terminator.

A split CRLF is one terminator, not a blank line. Counts do not infer that an
existing separator is accidental. Edits with none of these effects emit no advisory,
including multiline edits and ordinary whole-row newline preservation. No-op mutations emit none. Counts
aggregate multiple matches into one line.

The initial `file PATH` establishes the reference display context. Another `file PATH`
header appears only when references, a fallback preview, or formatter output switch to
another path. Display context does not select a file for editing. Paths are escaped as before; row identities and aliases are unchanged.

One `refs` block follows for every effective content-mutating command on every surviving
edited file. `COMMAND` is the command's positive one-based nonblank script index and `OP`
is its authored mutation operation. Blocks inherit the latest display path rather than
repeating it, and retain authored command order. Each block contains at most four distinct
current rows, ordered by final line number: the rows containing the first and last endpoints of
the command's aggregate rendered edit extent, the immediately preceding surviving row,
and the immediately following surviving row. Missing neighbors are omitted. Coincident
endpoint or context rows are emitted once within that block. A row may appear in separate
blocks when it identifies the context of separate source commands.

The projector derives each aggregate extent from that command's effective editor splices
in rendered final content, then maps the whole extent through language-formatting offsets,
including interior anchors that import sorting moves beyond the original endpoints. Import
spans carry their leading inline comments, indentation, and final row terminator. Replacement
aliases use an exclusive end; report endpoint rows retain the surviving boundary row.
A collapsed deletion endpoint maps to its surviving containing row; its available
neighboring rows provide boundary anchors. Logical-line clamping does not invent a
trailing empty row for a final terminator. An empty surviving file reports row `1` with
the hash of empty content. When the last command's file has no `refs` block, the report
retains the existing fallback of up to three rows from the start of that file without a
`refs` header, even when other surviving files have reference blocks.

Every row has `REQ-READ-001` identity over the complete current final logical line.
Outside the full formatter blocks above, `TEXT` contains at most the first 64 Unicode code
points of line content, without a line terminator or added ellipsis. Leading spaces are escaped as `\x20`, leading tabs as `\t`, and
all controls use their Go quoted form so indentation is visible and each row stays on one
report line. The hash still covers the complete untruncated content.
Outside formatter sections, the projection is bounded by four rows per effective command,
plus the three-row fallback; it does not retain another original or final content copy,
routed-read history, a word
diff, or translated patch text.

A successful report's `LINE:HASH` rows are current references for their named final paths
and may be used directly in the next invocation. An earlier row whose content is unchanged may
also be reused: its line is a hint and its hash relocates only when unique. The projection does
not guarantee every possible later target; when the exact target needed next is absent or
ambiguous, the caller obtains it with a focused hcat. A row or range endpoint is never guessed
or reconstructed. An in-process successful host result also carries one structured target alias
for every effective nonempty `type` command whose authored target is a row or inclusive row range.
The alias maps that exact target and final path to the final rendered replacement extent after
language formatting. Deletions, insertions, text-occurrence targets, appends,
and ineffective commands produce no alias. Root APIs retain no target or editing state between invocations.

For host variants, the complete report is rendered before commit or patch return. Apply host
variants return it only after the external effect succeeds; router emission is auxiliary and
cannot retroactively change or roll back a successful effect. Basic `Apply` discards the host-only
report and structured state at its public boundary.

Callers own coordination between writers to overlapping files and lifecycle paths, including
create and move destinations. Coordination covers the reads used to author an edit, evaluation,
and the complete application or rollback sequence. For translated edits it must continue until
the host executor finishes applying the patch; returning a patch does not reserve its baseline.
Callers may serialize overlapping work or assign non-overlapping ownership. The library and
router add no workspace writer lock, commit-time baseline comparison, or automatic rebase.
An immutable invocation baseline is an in-memory evaluation rule, not a cross-file filesystem
snapshot. If another writer changes a touched path outside this coordination contract, its
changes can be overwritten without a stale-target rejection.

Root application stages new contents in same-directory temporary files before starting the commit. Parse, validation, read, and evaluation failures leave the tree unchanged by mekugi.
A staging failure attempts to remove all temporary artifacts; cleanup failure returns
nonzero and identifies every artifact it could not remove. Commit-time filesystem failures
trigger rollback attempts using staged backups. Ordinary filesystems cannot provide a
portable crash-atomic transaction over multiple paths: termination, machine failure, or
rollback failure during commit can leave a partial change set. Such a failure must return
nonzero and name the affected paths; it must never report success or claim rollback
succeeded when it did not. Existing file permission bits are preserved; files created by
`new` use mode `0644`.

Atomic validation means that no script command publishes an intermediate edit. It does not
mean that external readers observe all changed paths at once: staging, installation, and
rollback use sequential filesystem operations. Callers requiring a consistent multi-file read
must coordinate those readers too. Cancellation observed before entering staging and commit
prevents application; cancellation during that sequence does not interrupt it. A host API can
return late cancellation after applying changes. An application error therefore does not imply
that no files changed; callers must inspect the outcome and workspace before retrying.

Host patch translation does not extend root application's staging or rollback
mechanism to Codex. Native host application may write files sequentially and fail
after changing an earlier file. Host refusal before mutation, partial application,
and interruption with an unknown outcome must not be collapsed into a blanket
"workspace unchanged" guarantee. A translated patch or pre-rendered report is not
confirmation of application; only host-confirmed success permits a routed
application success report. A validated no-op may report no changes without
host application, but must not claim a patch was applied. These distinctions
apply independently to each edit invocation.

OpenAI `apply_patch` is a logical-line format. Translation returns LF-only patch text
and normalizes line endings only in its displayed before/after lines; it does not
modify source files. The host's legacy update mode normalizes files to LF and can
collapse blank lines at EOF. Its line-ending-preservation mode retains those blank
lines and existing line endings. New files and updated final lines receive a final
terminator; an explicitly unterminated result is not byte-representable through
this format. Direct root application remains byte-exact outside the documented
language-aware finalization. Compatibility with the native executor's
line-ending-preservation mode is required separately from direct engine application.

Basic `Apply` returns errors for failures. Host variants place generic diagnostics
and structured failure data in `HostTranslation`; rendered generic diagnostics use the `mekugi:`
prefix. Command failures have the stable rendered form:

```text
OP: command N[, path "PATH"], reason REASON: MESSAGE
```

The visible command line omits source line, a repeated operation field, and category.
Structured host rejection data retain command index, source line, operation, path, generated
position, and localized value row when applicable; hook data also retain category. Validation
orders failures by command index and then localized value row. It emits one visible command
line per originating command and path. A command with several distinct repair locations uses
the message `N distinct syntax failures`, followed by bounded repair context for every
location; structured host data contain one rejection entry per location. Duplicate parser
messages that resolve to the same command and physical value row, or to the same inline script
row, remain one visible location. Independently parseable syntax failures may be reported
together before evaluation. A heredoc failure is owned by its header and may additionally
report its attributable source span. Control bytes are escaped and embedded newlines are
folded so one command failure remains one logical line.
Failures return no completed patch, patch summary, final-state report, target aliases, or review diffs.
The prepared success projection is published only after finalization succeeds, including
its final cancellation check. A late cancellation after application retains honest
`Outcome` and `Change.Applied` metadata without publishing reusable success references.
Basic entry points return an error; host variants return `HostTranslation` diagnostics.
Malformed row syntax
receives a syntax diagnostic.

A stale row reports the actual current-line candidate and up to two neighboring baseline rows.
It also reports every baseline line whose hash makes the stale reference ambiguous, or states
that the hash is absent. A unique relocated hash resolves during evaluation and does not produce
a diagnostic. Range repair reports start and end independently. When both requested coordinates
are in bounds and ordered, it also renders one explicitly unverified current-coordinate range
candidate in exact target syntax with its inclusive span length; normal endpoint verification
remains authoritative. A missing literal occurrence
reports the verified anchor context. An edit conflict identifies the prior command and affected
immutable-baseline lines. If
a command depends on content introduced by another command, the diagnostic directs the agent to
apply the prerequisite independently, reread, and submit a later invocation. A missing row or
failure without a verified baseline does not choose repair context. Repair context is
supplementary: it never changes the host outcome, mutation, or returned patch.
When invalid generated source is localized to a multiline mutation, each distinct rejection
identity includes the non-sensitive `value_line`. Transient root diagnostics describe every
bounded value-row context rather than mutation addresses. Routed target-only recovery diagnostics
add current short command handles only when every rejection is `row-stale`; other failures expose
no recovery handle under `REQ-CORRECT-001`.

The public host result separates lifecycle `Outcome`, requested `Change`, routed `Attempt`,
actionable `Failures`, durable-safe `Rejections`, and `PatchSummary`. A valid no-op returns
`evaluated/already-satisfied`, sets `Change.AlreadySatisfied`, and has an empty patch. Failure
scope is `field-local`, `multi-command`, `new-script`, or `new-transaction`; suggestions contain
bounded existing repair context rather than inventing new validation rules.

Acceptance:

1. Basic `Apply` returns only an error after commit. Host variants return `HostTranslation`,
   including the rendered final- or pending-state report. An already-satisfied translation succeeds
   with an empty patch and returns the rendered already-satisfied state.
2. Active paths, bounded last-mutation ranges, per-command final-reference blocks, net file
   counts, Unicode columns, truncation, control escaping, moved files, deletions, and empty
   files produce the specified report without implying cross-invocation persistence.
3. One invocation editing multiple regions and files reports current final paths and rows
   for every effective content command in authored order. A later invocation can target an
   exact reported row without hcat, while an unreported target requires a focused read and
   a saved pre-edit row still rejects as stale.
4. Changed Go files are formatted with the standard library before output, and invalid Go
   rejects the transaction without mutation. Literal normalization, comment rewriting, and
   import sorting or deduplication preserve usable final report rows and replacement aliases;
   supported changed Python, JavaScript, and TypeScript files are syntax-checked and receive
   supported automatic indentation correction. Other authored whitespace remains intact,
   including Markdown hard breaks, string and fixture content, and explicit EOF blank lines.
5. Malformed input, missing, stale, reversed, or incomplete targets, edit conflicts,
   unknown or future commands, invalid UTF-8, missing or non-regular files, path collisions,
   staging failure, translation failure, and cancellation observed before staging/commit produce
   no workspace mutation, returned patch, or final-state report, subject to the external-failure
   and temporary-artifact cleanup rules above.
6. Injected external filesystem commit and rollback failures are reported without false
   atomicity claims and without a successful final-state report.
7. Failure to emit a fully rendered routed report after a successful external effect does not reverse that effect or record a complete report-input token estimate.
8. Stale rows, incomplete literal targets, and edit conflicts emit verified repair context;
   a missing row fails without guessing, and a failure with no active baseline emits its
   diagnostic alone.
9. Invalid Go localized inside a heredoc value reports its
   physical body row in bounded repair context and structured host rejection identity
   without retaining body text.
10. One syntax-validation rejection includes every distinct actionable repair location from
    all changed files, groups visible diagnostics once per originating command and path,
    deduplicates parser cascades by repair row, and exposes enough current rejected-script rows
    for one atomic recovery payload to repair all locations.
