# Stock editing and execution

## REQ-EXECUTION-001 — Preserve Codex's execution authority

In Mekugi mode, the Codex request keeps its stock Code Mode JavaScript
`functions.exec` tool or native `apply_patch` and `exec_command` tools. The
router does not replace their names, schemas, arguments, results, or execution
path. Code Mode batching, including `Promise.all`, remains available. Codex
owns permissions, sandboxing, command processes, yielded sessions, and
`write_stdin` continuation. Mekugi never reruns a stock call while observing,
replaying, or displaying it.

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

A complete `apply_patch` argument stream may produce a provisional live diff
before Codex executes it. An unfinished argument or preview has no application
status. After Codex returns a result and the workspace outcome is observable,
Mekugi records evidence under [REQ-CHANGES-001](changes.md). The stock tool
result is forwarded unchanged, including errors. A failed call may have a
partial workspace effect, but it never publishes a successful edit receipt.

The live stream may also display stock `cat` heredoc writes and interpreter
programs extracted from `exec_command` or Code Mode. This projection is for
visibility only: it does not execute the command, create change evidence by
itself, or claim success before the host result. Commands continue to use
Codex's normal PTY, yield timing, environment, workdir, and session IDs.

Acceptance:

1. Direct native `apply_patch` and `exec_command` pass through with their
   original arguments and results. The same holds for Code Mode calls.
2. A Code Mode cell can batch or parallelize stock tools, including a patch
   alongside an independent command, without router-side serial execution.
3. Streaming patch input produces an early provisional preview; incomplete
   calls produce no successful durable change.
4. Successful, failed, and partial patch outcomes yield truthful change
   evidence, and a dependent `mchanges` read sees only persisted records.
5. Stock `cat`, interpreter previews, command labels, PTY/yield behavior, and
   `write_stdin` continuation remain available through the stock path. Wait
   arguments pass through without router-imposed floors.
6. Configured and built-in frontends use one authenticated snapshot and the
   Codex-owned executor, with no alternate tool carrier or MCP layer.
