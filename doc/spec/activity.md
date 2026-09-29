# Observed tool activity

## REQ-ACTIVITY-001 — Tool classification and observation

The [native UI](native_ui.md) consumes app-server events for live activity and
history. These shared classification rules describe operations, not generated
conversation messages. [Activity presentation](activity_display.md) owns row
layout; [session notices](notices.md) own actionable diagnostics. Journals own
[authored progress](journal.md). No activity observer generates conversation commentary.

### Native event authority

Codex app-server supplies command, edit, collaboration, web, image, and other tool
items for Main and child Activity. Completed host items and turn events establish
execution state. The router does not regenerate operation messages from provider
Responses calls, interpret arbitrary Code Mode output as session evidence, or
reconstruct a parallel activity lifecycle from request history.

Nested Code Mode tool activity follows the host's event stream. Independent
`Promise.allSettled` calls remain parallel; presentation cannot serialize their
execution. Static source recognition used for [execution previews](execution.md)
and [change capture](changes.md) remains separate from native activity delivery.
The host emits no item for a Code Mode cell itself. When a new cell result carries
the host's `Script failed` header, the router adds one error row to that agent's
activity with the first line of the host's trailing script error, so a script that
fails before any nested tool call still shows. The result reaches the model
unchanged, and failures from earlier requests are not shown again. Resumed
history restores these rows from the retained rollout under
[native UI resume](native_ui.md).
When a finished cell's result carries nothing beyond that header (and a failure's
script error), the model saw none of its nested results. Each nested
`exec_command` row with output then adds a muted `output not returned to the model`
note, identified through the native trace. A yielded cell, or one that printed
anything, adds no note. These notes last for the router's lifetime; restored
history does not show them.

### Shell command labels

A simple `cat`, valid `mcat` read, bounded `sed -n` print, `nl -ba FILE`, or
single-file `cat FILE | sed -n RANGES` or `nl -ba FILE | sed -n RANGES`
selection is labeled `Read`; literal `rg` is `Search`;
simple listings are `List`; and `inspect_file`, including its options and multiple
paths, is `Inspect`. Path globs and simple shell parameter references such as
`$HOME` and `${HOME}` are preserved as source without expansion; parameter
operators and executable substitutions normally remain `Run`. Bounded `sed -n`
reads are classified by their literal flags and print program, regardless of how
the input path is constructed. Dynamic operands remain visible verbatim and are
never evaluated by the display; `Read` describes the outer sed operation, not
the effects of any embedded substitutions. In-place edits and non-print scripts
do not qualify as reads.
A `cat` read piped to a bounded `head` retains its read label. `skills-mgr get` and `skills-mgr run` use `Skill`, including reference reads.
The skill name is bold; reference paths and run arguments retain normal weight.
`&&` chains show classified operations and unclassified `Run` neighbors in source
order, without claiming execution or success. Other unsupported compound commands
retain a `Run` preview instead of claiming a simpler operation. Mixed command scripts keep
every classified operation and show unclassified neighbors as `Run` in order.
`rg` and `grep` search previews show the query and target, omitting execution
flags and recognized output-only pipeline helpers such as `head`. Unknown or
effectful pipeline stages retain the original `Run` preview. Shell execution
still receives the exact original command, including flags and pipes.
Per-command `Run` excerpts omit statement-terminating semicolons; quoted
semicolons and other executable syntax remain visible.
Literal `printf` and `echo` section headings alongside classified operations are
omitted as display decoration; standalone, redirected, escaped, or dynamic headings
remain `Run` operations.
Literal `mread` recovery calls omit activity entries rather than appearing as `Run`.
Wait and input presentation follows typed host events and their command/item
identity. The activity observer does not reconstruct process or Code Mode cell
state from request history, guess a command for an uncorrelated poll, or treat
printed output as continuation evidence.

### Program and edit previews

Whole-program previews use fenced code blocks with the selected interpreter
language. Literal interpreter wrappers, including `python -c`,
`python - <<'PY'`, and `node -e`, show a multi-line program rather than a Bash
wrapper, under a header naming the command with `…` for the program, as
`Ran python3 -c …` or `Ran python3 -`. A one-line program shows as the literal
command, which names its interpreter. A lone `-` with nothing redirected to
stdin adds a muted `· program read from stdin`. This same projector supplies provisional streaming previews and completed
operation displays. It never evaluates shell expansions or implies that a
command succeeded. Source line breaks and indentation remain intact.

Literal, statically scoped `sed -i` edits use the shared capture parser to show
`Edit` intent immediately, without waiting for a retained receipt. This requested
operation has no applied counts; the captured `mchanges` evidence later replaces
it in place with the observed file changes. Read-only `sed` is not an edit.

Stock `apply_patch`, including transparent Code Mode calls using immutable
literal patch bindings, does not emit a bare `Edit` label, a generic `Run`
preview, or its patch body into child activity. After the host result and
workspace outcome are recorded, authenticated successful edit receipts
classify each changed path as
`Create`, `Edit`, `Delete`, or `Move` with added and removed line counts and a
bounded diff of the observed hunks. A completed transparent Code Mode cell
reports its complete observed workspace effect the same way, without claiming
nested patch success.
Classification uses the same review files as `mchanges`; it does not guess from
the command text. Paths inside the workspace display relatively; outside paths
remain absolute. Incomplete captures show unavailable counts. Failed and
unfinished patches produce no successful edit summary, and repeated receipts
are deduplicated. The live pane separately owns full provisional and completed
diff display under [REQ-CHANGES-001](changes.md).

### Other tools and grouping

Native MCP items retain server/tool identity; other tool items use their typed
host identity and available arguments. Unknown calls remain visible without claiming
an inferred operation. [Native activity presentation](activity_display.md) owns grouping,
clipping, and agent headings. Source item identity deduplicates live and restored
activity without altering provider responses or the host's replay history.

Acceptance:

1. Native calls keep their original inputs, results, execution order, and continuation
   authority while their host events receive useful labels.
2. Parallel `Promise.allSettled` operations remain concurrent. Host event identity,
   not guessed JavaScript or printed results, establishes their activity state.
3. Edit intent is not successful edit evidence. Confirmed summaries require retained
   host results and observed workspace effects under [changes](changes.md).
4. Unknown calls preserve their identity and arguments; projection neither executes
   source nor claims agent completion.
