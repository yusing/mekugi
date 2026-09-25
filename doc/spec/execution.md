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
path. Code Mode batching, including `Promise.all`, remains available. Codex
owns permissions, sandboxing, command processes, yielded sessions, and
`write_stdin` continuation. Mekugi never reruns a stock call while observing,
replaying, or displaying it.

The credential-gated [explore output filter](explore_filter.md) is the sole
exception to unchanged model-visible result text. It projects eligible completed
native and transparent single-call Code Mode results, including command lists,
retains their original output before exposing recovery, and never alters
execution or continuation.

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
Post-result change sweeps are bounded auxiliary observation, not execution hooks.
They do not wrap commands, inject environments, or alter yielded-session handling.
Command change notices in model-visible stock output remain deferred; results stay
unchanged.

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
