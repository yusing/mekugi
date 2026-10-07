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

In Mekugi mode, launches require Codex's JavaScript `exec` interface. The wrapper
forces invocation-local `features.code_mode=true` and `features.code_mode_only=true`
after caller overrides. Models that advertise top-level `apply_patch` or
`exec_command` instead receive an unsupported-interface error before inference.
Stock `apply_patch` and `exec_command` execute through the nested tool catalog;
the router does not replace their names, schemas, arguments, host results, or execution
path. Model-visible output uses the
[duplicate-output projection](#duplicate-output-projection) below. The execution
exceptions are the in-shell segment tracking of
[REQ-EXECUTION-002](#req-execution-002--track-each-segment-of-a-command-list), which
keeps the result and the Codex-owned process lifecycle unchanged, and the remote
write guard of
[REQ-EXECUTION-003](#req-execution-003--guard-remote-vcs-writes), which can fail one
guarded command. Dash-backed sh tracking and the enabled guard use Codex's native
`PreToolUse.updatedInput` hook to instrument shell command text. This is a narrow
exception to byte-identical command input: tool identity, all other arguments,
selected shell, results, permissions, sandbox and continuations stay Codex-owned.
`exec` batching, including `Promise.allSettled`, remains available. Codex
owns permissions, sandboxing, command processes, yielded sessions, and
`write_stdin` continuation. Mekugi never reruns a stock call while observing,
replaying, or displaying it.

Completed edit observations may append a separate
agent-visible change-ID and summary text part as specified in
[applied changes](changes.md), preserving the original host result content.

Native command approvals retain Codex's offered session, command-prefix and
permission scopes. In the proxied UI, command denial uses native decline so
the turn continues without running the command. The original native tool
rejection remains intact, with no synthetic shell exit code. Companion user
feedback reports denial with or without the optional reason in the existing
provider continuation for the same thread, turn and tool roundtrip; it neither
steers the host nor starts a new turn. File-change and permission approval
choices remain unchanged. Proxy-free direct UI and passthrough preserve the
original host decisions and do not offer typed denial reasons. Dock editing and
draft preservation are specified under [native UI approvals](native_ui.md#approvals).

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

A streaming `apply_patch` argument, including a decoded JavaScript string literal,
produces a provisional live diff as patch text arrives, without waiting for the
closing quote, call, or end marker. An unfinished argument or preview has no application
status. After Codex returns a result and the workspace outcome is observable,
Mekugi records evidence under [REQ-CHANGES-001](changes.md). The stock tool
result is forwarded unchanged, including errors, except for the model-visible
duplicate-output projection. A failed call may have a
partial workspace effect, but it never publishes a successful edit receipt.

Command observation under [REQ-CHANGES-001](changes.md) reads the completed
`exec_command` arguments or literal nested command text. The forwarded call
stays byte-identical through observation; command tracking or the approval guard
may subsequently instrument command text through the native hook described above. Pre-call capture
is time-bounded so that an unreadable scope becomes incomplete evidence rather
than delaying Codex.
Post-result comparisons read only known edit operands, never a workspace sweep.
They do not wrap commands, inject environments, or alter yielded-session handling.
Agent-visible change notices are appended only after the observed evidence is
durable; they do not replace original stock output. The separate duplicate-output
projection may reference repeated host text, but leaves these notices intact.

Wrapped Mekugi launches enable Codex's native rollout trace in a private,
session-scoped temporary directory. Observation joins the executing thread,
outer call and source, `exec` cell, and individual tool results. It does not
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
including JavaScript source and nested command arguments. Final content and
the streaming-complete marker share one snapshot, so coalescing cannot leave a
truncated last frame. This remains auxiliary projection, not tool completion or
execution evidence; interrupted requests without completed input do not flush
a fabricated final script.

The live stream may also display stock `cat` heredoc writes, literal file
operations, and interpreter programs extracted from nested `exec_command` calls. This projection is for
visibility only: it does not execute the command, create change evidence by
itself, or claim success before the host result. Commands continue to use
Codex's normal PTY, yield timing, environment, workdir, and session IDs.
Running scope polling is display-only and cannot finalize a command, revive a
continuation, or contribute predicted bytes to saved evidence. Finalization still
requires the terminal host result and post-result reconciliation.

Acceptance:

1. Nested stock `apply_patch` and `exec_command` pass through with their
   original arguments and results, except for the eligible model-visible output
   [projection](#duplicate-output-projection) and native command instrumentation
   above.
   Unsupported top-level execution catalogs fail before inference.
2. An `exec` cell can batch or parallelize stock tools, including a patch
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
7. Native command denial preserves the host tool rejection and delivers optional
   user feedback only to the matching provider continuation, without a synthetic
   exit code, steering or a new turn. Host-offered approval scopes, file-change
   and permission choices remain available; proxy-free clients retain original
   host decisions without reason editing.

### Duplicate-output projection

Duplicate-output projection is opt-in in Mekugi mode. Enable it with the launch
option `--duplicate-output`; it is off by default. It changes only eligible text
in the model-visible request input. Passthrough is unchanged. The original request
input, host result, Codex rollout, retained replay and change evidence, and UI
keep the full text. Every evidence consumer, including command and patch
observation, result confirmation, failure observation, retention, and continuation
advice, reads the original output before this final projection.

A unit is one host-produced text part of a `function_call_output` or
`custom_tool_call_output`, or one submitted file or managed-skill frame body in a
user message through a complete durable envelope in an `input_text` part.
A string output is one part. Execution and attachment headers remain
verbatim. Durable attachment envelopes use their submitted bodies; files and skills
are never reopened to infer equality. Frame byte ranges describe the original
snapshot, while reference rows address the earlier visible body. Ordinary user
requests, base instructions, selection attachments, malformed envelopes, and media
are excluded. String-content or non-`input_text` envelopes and bare frame text
remain ordinary input. Native selected-skill XML retains its existing manager
rewrite. Router-appended parts, including journal IDs, warnings, edit notices,
and continuation advice, remain intact. Outputs without a call ID and native
live-session JSON envelopes are excluded.

Verbatim runs aligned to logical lines and covering at least 192 bytes may be
replaced with a reference to earlier output still visible in the same request.
Matching is content-based, without line-ending or line-number normalization.
The bounded matching window covers the last 256 KiB of indexed verbatim text;
below-threshold units are neither replaced nor indexed. The longest matching run
wins, with ties resolved in favor of the most recent source.

References have the form `[same as O3]` or `[same as O3 L40-112]`; a single row
uses `L40`. Each eligible unit with remaining verbatim text gets a source anchor
such as `[O3]`, on its own line after the preserved execution or attachment header.
IDs are assigned in forward order in one request-local sequence across tool text
parts and attachment frames. Fully replaced units get no anchor. Sources get their
anchors immediately, even before a later repeat, so extending the input preserves
earlier projected items.

The row range is omitted for an entire earlier body. Rows start at 1 after the
anchor and count the earlier body as delivered, with each generated reference
counting as one row. References always target visible verbatim text, never anchors,
generated references or replaced spans, so they need no second hop.
Literal host text resembling a marker remains host text.

Each projection is a fresh forward pass over the current original input, with
no persistent matcher state. Compacted, forked, and resumed views can reference
only their own current input. Fresh projections of the same original host input
are byte-identical, and extending that input preserves earlier projected items.
The [transport contract](transport.md) owns provider-history reconciliation.

## REQ-EXECUTION-002 — Track each segment of a command list

In the UI, a supported Bash command or Linux `/bin/sh` command backed by dash
whose script is a top-level list (`;`, newline, `&&`, `||`) reports each segment's
own output, exit status,
start/end timestamps and elapsed duration, so Activity can show every segment with
its own state and stream live output. Clicking a segment opens only its retained
stdout and stderr in the shared output dialog. Supported single commands,
including pipelines, report their own elapsed duration without redirecting
stdout or stderr. Bash also supports timed single commands. Pipelines and compound
commands are single segments. Authored command text, stdin, output bytes, exit status,
PTY/yield behavior, and `write_stdin` continuation stay those of the stock
command. Codex's startup, cancellation, sandbox, and process group apply
unchanged.

For Bash, the launcher adds a hook to the startup file that the frontend PATH
already uses. The hook runs in the command shell Codex started, after login
startup, so profile functions and aliases remain available. It never evaluates
the script or starts a second shell for it. The helper `mekugi-exec`, installed
beside `mekugi`, splits the script with the router's own splitter and asks the
router to match it to a live `commandExecution` item by thread and exact script.
Tracking uses existing FIFOs and router-created per-command resources, so it
works under Codex's read-only and workspace-write sandboxes without network
access or filesystem creation by the helper. Only after a match and acquisition
of the required resources does the shell accept tracking. A single command runs
in place with begin and EXIT observations, leaving its original output descriptors
untouched. For a list, the shell runs an instrumented copy in place of the script
and exits. A one-time trap runs the copy
before the script's first command, so Bash's own error messages, line numbers,
and fatal errors are those of the original. The copy wraps each segment in hooks that preserve its status,
`$?`, and `set -e`. In every other case the hook returns before any segment runs
and Bash runs the original script, so no script runs twice. Missing startup
resources leave the original script to run once; after tracking is accepted,
router cancellation cannot suppress its execution. Codex still owns command
cancellation.

For dash-backed sh, Codex's native `PreToolUse` command hook adds inline
instrumentation to supported POSIX scripts. It runs in Codex's selected shell,
without launching a substitute interpreter, and uses the same segment reports,
measured timing, output retention and UI as Bash. Bash keeps its startup path.
The native hook is registered for tracking even with `--vcs-guard=false`.
With the VCS guard disabled, explicit caller `PreToolUse` conflicts leave caller
settings intact and sh untracked; an enabled guard rejects a conflicting launch.
Missing or incompatible hook resources leave commands running once
with aggregate host output. This support is limited to Linux dash-backed sh;
other sh implementations are not promised segment tracking.

A script stays untracked when:

- it is a list that starts with a subshell or a timed command;
- it uses job control, traps, `coproc`, a top-level `return`, `exec` with a
  program, command tracing, or variables that name the running command;
- it is a Codex shell-snapshot script or wrapper (the wrapper's inner shell is
  tracked instead);
- it runs in a nested shell started by a command;
- for sh, it uses transport descriptors 7 or 8, computed descriptor duplicates,
  or inherits an open descriptor 3, 4, 7 or 8;
- it does not match a live item within 350 ms, allowing for Codex's early-exit
  grace period and notification delivery, for example because Codex
  redacted a secret-like word in the displayed command;
- multiple unmatched live items could own the same report;
- the helper, tracking channel, router, or required startup resources are
  unavailable.

Scripts that inspect instrumentation-sensitive shell variables are skipped before
helper startup.

For a nonterminal list, the helper relays the shell's output to Codex's original
descriptors while it reports each segment's share. The shell waits for the helper to acknowledge
each segment's begin and end, so output cannot precede its segment identity or
spill across a completed segment boundary. At these control boundaries the helper
records wall-clock timestamps and measures elapsed time with its monotonic clock,
after setup and matching, before draining end-of-command output. For a single
command, elapsed time spans its begin and EXIT observations, including the small
boundary and acknowledgment overhead. The shell's EXIT boundary closes the active
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
without promising restoration. The owning thread receives composer error feedback
with the host turn/item identity and complete underlying error. Distinct causes
remain separate; the [notice contract](notices.md) owns access to full details,
dismissal, delivery and launcher recovery.

The same matching, host-exit validation, retention and history restoration apply
to single-command reports. Old history without reports, missing or mismatched
records, and incomplete reports keep the host timing and combined result.
Single-command reports retain host output unchanged. Terminal and lossy reports
retain their observed segment statuses but not separate output. If any segment's bounded
output was truncated or released before persistence, restored output likewise
stays combined rather than presenting a partial stream as complete. Activity and
output dialogs hide literal host shell wrappers and generated VCS guard
instrumentation, preserving user PATH assignments and other shell source.
This display projection leaves guard execution and stored commands unchanged.
The per-command overhead is one reporting helper start and two acknowledgments
per segment, plus managed storage of a completed report. A supported single
command uses begin/EXIT acknowledgments. Dash-backed sh adds a startup helper
call before the reporting helper; measured command time excludes this setup.

Acceptance:

1. Tracked and untracked runs of the same list produce byte-identical stdout
   and stderr and the same exit status. Covered cases: `$?` across segments,
   `cd`, `exit N`, `set -e` with `||` and `!`, heredocs, short-circuited
   `&&`/`||`, native shell error messages, and fatal expansion errors, in both
   Bash and dash-backed sh.
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
   authored script identity after removing exact PATH-prefix instrumentation for
   the current session's VCS guard. Helper wrappers, unrecognized source, and
   other-session source stay intact;
   ambiguous matches are rejected. Report matching and host inputs remain unchanged.
   These live observations never
   finalize the host command or create durable change receipts.
7. Completed segment output, timing, failure and skipped states survive a fresh router
   and inherited host history. Changed workspace, turn, item, command, aggregate
   or exit cannot borrow another invocation's report. Missing evidence cannot
   manufacture output boundaries.
8. Installed Codex under its read-only and workspace-write sandboxes, without
   network access, reports each segment's stdout, stderr, exit and skipped state.
   Missing startup resources run the original script once; router cancellation
   after resource acquisition does not prevent accepted execution.
9. Supported single commands, including `time skills-mgr get user-experience`
   and pipelines, run in place with unchanged output descriptors and host exit
   status. Their elapsed suffix uses observed begin/EXIT duration, excluding
   setup and matching. Unsupported or missing observations retain host timing
   and output fallback; no extra timing metadata rows appear in the dialog.

## REQ-EXECUTION-003 — Guard remote VCS writes

Interactive UI launches guard remote version-control writes by default,
independently of Codex's approval policy, including with `--yolo`. Writes wait
for the user's approval in the UI. This covers Git, GitHub CLI, Mercurial,
Subversion and Jujutsu, without restricting the remote, ref or tag being written.
The Mekugi flag `--vcs-guard=false`, placed before `codex` or standalone Codex
arguments, disables only this guard, preserving Codex's approval policy and
command tracking. Headless and noninteractive commands have no guard.

The guard requires unsandboxed command execution: configure
`sandbox_mode="danger-full-access"` or use `--yolo`. The latter disables Codex
approvals and sandboxing, not this guard. Sandboxed guard execution is unsupported.
Mekugi does not silently change the user's configuration, approval policy or
sandbox settings.

The guard uses Codex's native command hook to instrument shell command text
before execution. It leaves expansion and control flow to Codex's selected
shell, including Bash, sh and zsh, rather than selecting Bash or running the
whole script in a substitute interpreter. It preserves all other tool arguments,
permissions, sandbox settings, process ownership and continuation semantics.
A command asks only when execution reaches it, not for a skipped branch.
Approved writes and non-writes run the selected real executable with its expanded
arguments, output and exit status.

The guard offers approval once, approval for the exact expanded argument list,
workdir and resolved executable for the current UI session, or denial. Session
approval also resolves queued requests with the same identity; another argument,
workdir or executable asks again. These grants have no command-prefix scope and
expire with the UI, rather than persisting across resume. They are separate from
Codex's native approval scopes.

Direct commands and supported wrappers (`env`, `command`, `exec`, `nohup`,
`timeout`, `nice` and `xargs`) recognize guarded tools by name or executable
path. Absolute and relative paths may contain spaces and need not be on PATH;
expanded directory paths such as `"$tools/git"` are recognized by their tool-name
suffix. Nested `sh`, `bash`, `dash`, `zsh` and `ksh` command-string invocations
are instrumented after their `-c` payload expands. Bare tool and shell names
retain shell-function lookup and use the command-local PATH; `command -p`
retains its default-path lookup.

The enabled guard requires Mekugi mode, the installed `mekugi-exec` sibling,
Codex's native command-hook support and the approval channel. Before each new
user turn, the UI verifies the workspace's effective hooks. A missing, disabled
or untrusted guard, or a competing trusted synchronous shell hook, blocks the
turn with an explanation and preserves the draft. Explicit CLI overrides of
`hooks` or `hooks.PreToolUse` prevent a guarded launch. Interactive passthrough
requires explicit `--vcs-guard=false`, rather than `--yolo`. Invocation-local hook
setup preserves recovery-hook trust and leaves user configuration files unchanged.

Denial, no answer within 5 minutes, or a blocked or unreachable approval channel
prints `mekugi: remote write denied: REASON` to stderr and exits 1 without running the
write. Only that command fails: a `;` list continues, and `&&`/`||` follow the
selected shell's normal failure semantics, including its error-handling options.
If the command exits while waiting, the request is withdrawn. Approval does not
grant sandbox permissions.

A user denial without a reason prints
`mekugi: remote write denied: user denied this command without a reason`.
With a reason it prints
`mekugi: remote write denied: user denied this command with a reason: <reason>`.
Both exit 1 before invoking the real denied command. This is a shell-command
failure, unlike the native tool rejection of a Codex command approval.

Writes are:

- Git: `push`, `send-pack` and `send-email` unless the last dry-run option
  enables a dry run, `subtree push`, `svn dcommit`, `p4 submit`, and `lfs push`,
  `lock` and `unlock`. Git global options are skipped. Another subcommand outside
  the known read and local-write set resolves through the real tool's
  `alias.NAME` with the same global options; a shell alias's script is classified
  with its arguments, and a non-alias, which is an external command that may push
  through Git's exec-path, is a write.
  Because Git puts its exec-path ahead of PATH, scripts that nested Git would run
  unguarded (`submodule foreach`, `rebase -x`/`--exec`, `bisect run`) are
  classified up front.
- `gh`: commands outside a known read-only set, including unknown commands and
  extension commands; `clone` reads only for `repo` and `gist`; `api` with a non-GET method, request fields or input, or a
  GraphQL mutation or file-supplied query.
- `hg push`, `email` and `phabsend` (including unambiguous prefixes); `svn`
  `commit`, `import`, `lock`, `unlock`, revision-property changes and operations
  on repository URLs; `jj git push`, `jj gerrit upload`, and unknown `jj`
  commands, which may be aliases.

Git alias and embedded command scripts are classified through `env`, `command`,
`exec`, `nohup`, `time`, `timeout`, `xargs`, `nice` and `builtin` wrappers. A
dynamic word in subcommand position, or a script that does not parse, counts as
a write. Help and version invocations do not.

The guard protects against accidental remote writes, not arbitrary process
execution. Command-text instrumentation does not comprehensively cover fully
computed executable words without a recognized tool-path suffix, `eval`,
`env -S` split payloads, sourced files, script-file contents, or arbitrary
programs that internally execute absolute VCS paths. Bash and zsh startup
guards add PATH and known-absolute-path coverage inside scripts without changing
user startup files; they do not remove these limits. Parsing failures reported
by Mekugi's hook reject that tool call. Hook failures outside the handler follow
Codex's host failure policy and need not fail closed.

An approved command that runs another guarded write, such as `gh pr create`
pushing a branch through a guarded Git lookup, asks again for the inner command.

Acceptance:

1. With push denied, `git add -A; git commit -m x; git push origin main; git log`
   exits 0 with segment statuses 0, 0, 1, 0, and the `&&` form exits 1 with
   0, 0, 1 and `git log` skipped. The real tool never runs the push.
2. An approved write and every non-write run the real tool with unchanged
   arguments, output and status.
3. An unanswered request is denied after the timeout, whether or not the UI has
   received it, and the UI records the outcome.
4. Installed Codex with unsandboxed execution, including `--yolo`, runs the real
   helper to the native dock; denial fails only the push, and approval runs it.
   A blocked or unreachable connection denies the command.
   The same holds in Bash, sh and zsh, by tool name and by direct or
   supported-wrapper executable path,
   including expanded paths and paths with spaces.
5. A nested shell command string is checked after expansion. Command-local PATH,
   shell functions, `command -p`, environment assignments and shell options
   retain their native lookup and expansion behavior.
6. A missing, disabled or untrusted effective guard, or a competing trusted
   synchronous shell hook, blocks a new user turn without losing the draft or
   modifying user configuration.
7. The default guard remains enabled with `--yolo`. Explicit `--vcs-guard=false`
   removes remote-write prompts while preserving command tracking and the chosen
   Codex approval and sandbox policy. Interactive passthrough rejects an enabled
   guard and accepts the explicit opt-out.
8. Session approval releases exact queued duplicates and subsequent matching
   commands only within the same UI session. Changed arguments, workdir or
   resolved executable require another decision; closing and resuming clears
   the grant. Denial with or without a reason emits the specified stderr and
   exits 1 without running the real command.
