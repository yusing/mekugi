# Stock editing and execution

## Bounded command output: mrun

`mrun (-n N|--max-tokens N) [--tail] [--] COMMAND [ARG...]` runs a foreground
command under the host's existing process, stdin, cancellation, and continuation
authority. The first non-option operand starts the command; all subsequent
arguments belong to it. `--` explicitly marks the same boundary.

`-n` selects up to N rows per stream. `--max-tokens` accepts 1–15500 and bounds
the combined selected command text. When stdout is nonempty, stderr receives
at most half that budget; stdout receives the remainder. Token-window selection
may cut a row. `--tail` selects the ending instead of the beginning. Output
outside the selected window is discarded, not promised as recoverable.

Delivery is separately capped at 15500 tokens and ends on complete rows. Only
overflow of this delivery page is retained for `mread`; retained stderr consists
of command output, never the frontend's generated limit notice. The notice is
outside the command-output budget. The command's exit status is preserved even
when output is incomplete; invalid frontend arguments exit 2 and a missing
command exits 127.
Newline-terminated retained streams use complete-row continuation when each row
fits a maximum page including framing. Oversized rows and unterminated generic
streams use byte continuation instead, preserving recoverability without adding
or discarding command bytes. This exception does not change the initial delivery
page's complete-row boundary.

Acceptance: mixed streams retain stdout and bound stderr; a 15000-token window
needs no extra continuation solely due to the delivery cap; line-only delivery
overflow reconstructs exact selected output on row boundaries; both explicit
and implicit command boundaries preserve command arguments.

## REQ-EXECUTION-001 — Preserve Codex's execution authority

In Mekugi mode, the Codex request keeps its stock Code Mode JavaScript
`functions.exec` tool or native `apply_patch` and `exec_command` tools. The
router does not replace their names, schemas, arguments, results, or execution
path. The only exception is the in-shell segment tracking of
[REQ-EXECUTION-002](#req-execution-002--track-each-segment-of-a-command-list), which
keeps the result and the Codex-owned process lifecycle unchanged. Code Mode batching, including `Promise.allSettled`, remains available. Codex
owns permissions, sandboxing, command processes, yielded sessions, and
`write_stdin` continuation. Mekugi never reruns a stock call while observing,
replaying, or displaying it.

Completed edit observations may append a separate
agent-visible change-ID and summary text part as specified in
[applied changes](changes.md), preserving the original result content.

The wrapped Codex `PATH` includes only this session's authenticated executable
frontends. `mcat`, `mrun`, `mread`, `mchanges`, `msymbol`, `inspect_file`, and
configured plugin tools are invoked through stock `exec_command`. The pinned
snapshot and worker authenticate each frontend; a frontend does not install a
global command or create a second process-continuation authority.
The wrapper restores that private PATH after Bash login startup without changing
Codex's login-shell setting or user configuration. Executable frontend children
stay in the stock command's process group so Codex cancellation can terminate
them rather than leaving detached descendants. A dedicated Linux frontend also
reaps resolver orphans after an explicit cleanup result; on other platforms,
those descendants remain in the stock group until the command ends.

A streaming `apply_patch` argument, including a decoded Code Mode string literal,
produces a provisional live diff as patch text arrives, without waiting for the
closing quote, call, or end marker. An unfinished argument or preview has no application
status. After Codex returns a result and the workspace outcome is observable,
Mekugi records evidence under [REQ-CHANGES-001](changes.md). The stock tool
result is forwarded unchanged, including errors. A failed call may have a
partial workspace effect, but it never publishes a successful edit receipt.

Command observation under [REQ-CHANGES-001](changes.md) reads the completed
`exec_command` arguments or literal Code Mode command text. The forwarded call
stays byte-identical, and its pre-call capture is time-bounded so that an
unreadable scope becomes incomplete evidence rather than delaying Codex.
Post-result comparisons read only known edit operands, never a workspace sweep.
They do not wrap commands, inject environments, or alter yielded-session handling.
Agent-visible change notices are appended only after the observed evidence is
durable; they do not replace original stock output.

Wrapped Mekugi launches enable Codex's native rollout trace in a private,
session-scoped temporary directory. Observation joins the executing thread,
outer call and source, Code Mode cell, and individual tool results. It does not
wrap tools, change JavaScript, install execution hooks, or trust printed results.
Shell success requires a terminal exit code of zero, not merely a completed
dispatch. A yielded process stays unfinished until its native runtime result.
Matched receipts are persisted with the captured changes, so review after resume
does not depend on the temporary trace or revive a process.

Trace reading is incremental and bounded; missing, ambiguous, unsupported, or
corrupt evidence cannot confirm success. Codex's recorder itself currently has
no tool-only switch or storage cap and also records prompts and responses. Its
private directory is removed when the router session closes; abrupt process
termination can leave temporary files. This invocation-local override does not
change user configuration. Older captures without receipts remain reviewable
as observed effects rather than being upgraded to confirmed edits.
Scope providers may parse local source or issue a validated local read-only query.
They never evaluate interpreter source, run a writer stage, contact a remote, or
invoke configured hooks, preprocessors, filters, or monitors. Missing tools and
provider timeouts reduce evidence coverage without replacing the stock result.

Input completion flushes the authoritative final input through the preview worker,
including Code Mode JavaScript and native command arguments. Final content and
the streaming-complete marker share one snapshot, so coalescing cannot leave a
truncated last frame. This remains auxiliary projection, not tool completion or
execution evidence; interrupted requests without completed input do not flush
a fabricated final script.

The live stream may also display stock `cat` heredoc writes, literal file
operations, and interpreter programs extracted from `exec_command` or Code Mode. This projection is for
visibility only: it does not execute the command, create change evidence by
itself, or claim success before the host result. Commands continue to use
Codex's normal PTY, yield timing, environment, workdir, and session IDs.
Running scope polling is display-only and cannot finalize a command, revive a
continuation, or contribute predicted bytes to saved evidence. Finalization still
requires the terminal host result and post-result reconciliation.

Acceptance:

1. Direct native `apply_patch` and `exec_command` pass through with their
   original arguments and results, except for the eligible model-visible output
   projection above. The same holds for Code Mode calls.
2. A Code Mode cell can batch or parallelize stock tools, including a patch
   alongside an independent command, without router-side serial execution.
3. Streaming patch input produces an early provisional preview; incomplete
   calls produce no successful durable change.
4. Successful, failed, and partial patch and declared-command outcomes yield
   truthful change evidence, and a dependent `mchanges` read sees only
   persisted records.
5. Stock `cat`, interpreter previews, command labels, PTY/yield behavior, and
   `write_stdin` continuation remain available through the stock path. Wait
   arguments pass through without router-imposed floors.
6. Configured and built-in frontends use one authenticated snapshot and the
   Codex-owned executor, with no alternate tool carrier or MCP layer.

## REQ-EXECUTION-002 — Track each segment of a command list

In the native app-server UI, a Bash command whose script is a top-level list
(`;`, newline, `&&`, `||`) reports each segment's own output, exit status,
start/end timestamps and elapsed duration, so Activity can show every segment with
its own state. Pipelines and compound commands are single segments. Command text, stdin, output bytes, exit status,
PTY/yield behavior, and `write_stdin` continuation stay those of the stock
command. Codex's startup, cancellation, sandbox, and process group apply
unchanged.

The launcher adds a hook to the Bash startup file that the frontend PATH
already uses. The hook runs in the command shell Codex started, after login
startup, so profile functions and aliases remain available. It never evaluates
the script or starts a second shell for it. The helper `mekugi-exec`, installed
beside `mekugi`, splits the script with the router's own splitter and asks the
router to match it to a live `commandExecution` item by thread and exact script.
Only after a match does the shell run an instrumented copy in place of the
script and exit. A one-time trap runs the copy before the script's first
command, so Bash's own error messages, line numbers, and fatal errors are those
of the original. The copy wraps each segment in hooks that preserve its status,
`$?`, and `set -e`. In every other case the hook returns before any segment runs
and Bash runs the original script, so no script runs twice.

A script stays untracked when:

- it is a single command;
- it starts with a subshell or a timed command, which would run before the trap;
- it uses job control, traps, `coproc`, a top-level `return`, `exec` with a
  program, command tracing, or variables that name the running command;
- it is a Codex shell-snapshot script or wrapper (the wrapper's inner shell is
  tracked instead);
- it runs in a nested shell started by a command;
- it does not match a live item within 150 ms, for example because Codex
  redacted a secret-like word in the displayed command;
- multiple unmatched live items could own the same report;
- the helper, socket, or router is unavailable.

The helper relays the shell's output to Codex's original descriptors while it
reports each segment's share. The shell waits for the helper to acknowledge
each segment's begin and end, so output cannot precede its segment identity or
spill across a completed segment boundary. At these control boundaries the helper
records wall-clock timestamps and measures elapsed time with its monotonic clock,
before draining end-of-command output. The shell's EXIT boundary closes the active
segment if its ordinary end hook was bypassed. Missing boundaries never borrow
invocation duration. Protocol version 3 carries this timing with boundary reports.
Output a background descendant writes later is attributed to the segment that
is running when it arrives. A terminal command is not relayed, because
programs would detect a pipe; it reports statuses only and keeps the host's
combined output. The helper never blocks on the router. When reporting falls
behind or exceeds 4 MiB, it stops reporting output and continues relaying
unchanged.

A completed command shows its segments only when its report ended with the
host's own exit status. Otherwise, including a report that ends when the shell
replaces itself or is killed, Activity falls back to the host's combined result.
Completed, host-validated reports are retained in the managed replay store,
scoped to the workspace and exact host turn/item identity. Restored history,
including inherited fork history and `codex resume`, uses them only when the
command, aggregate output and terminal exit still match. Replay restores
settled output, states and observed timing, never a process or continuation.
Reading inherited reports retains them for the requesting thread independently
of the original thread. Retention runs outside the UI event loop with at most 32
pending reports and a 30-second storage-lock wait. Each pending report keeps its
original workspace and thread ownership even if the user switches sessions.
Normal exit drains accepted writes; cancellation cancels their lock waits.
Storage failure or a full pending-report bound leaves live presentation available
without promising restoration. The owning thread receives a native Main error
entry with the host turn/item identity and complete underlying error, wrapped in
the transcript rather than truncated into the composer. Distinct causes remain
separate, and notices not painted before exit remain available to the launcher.

Old history without reports, missing or mismatched records, and incomplete
reports keep the combined host result. Terminal and lossy reports retain their
observed segment statuses but not separate output. If any segment's bounded
output was truncated or released before persistence, restored output likewise
stays combined rather than presenting a partial stream as complete. The combined
output dialog hides only the literal host shell wrapper, using the same display
rules as Activity; stored commands and executed arguments remain unchanged.
The per-command overhead is one helper start and two acknowledgments per
segment, plus managed storage of a completed report. Single commands start no
helper.

Acceptance:

1. Tracked and untracked runs of the same list produce byte-identical stdout
   and stderr and the same exit status. Covered cases: `$?` across segments,
   `cd`, `exit N`, `set -e` with `||` and `!`, heredocs, short-circuited
   `&&`/`||`, Bash error messages, and fatal expansion errors.
2. Each segment's report carries only its own output and status. A
   short-circuited segment is reported as never run once a later segment starts
   or the shell finishes, and a shell that exits
   inside a segment attributes that exit to it.
3. Codex snapshot scripts, nested shells, unmatched scripts, and untrackable
   scripts run unmodified.
4. Terminal commands keep their terminal, and only statuses are reported.
5. An incomplete report never replaces the host's combined output or exit.
6. Edit intent and running diff cards use segment lifecycle rather than waiting
   for later non-edit commands. Preview matching requires exact thread, turn and
   script identity and rejects ambiguous matches. These live observations never
   finalize the host command or create durable change receipts.
7. Completed segment output, timing, failure and skipped states survive a fresh router
   and inherited host history. Changed workspace, turn, item, command, aggregate
   or exit cannot borrow another invocation's report. Missing evidence cannot
   manufacture output boundaries.
