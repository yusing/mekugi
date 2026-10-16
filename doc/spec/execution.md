# Stock editing and execution

## Bounded command output: mrun

The [bounded-command module](../../modules/boundedcommand/SPEC.md) owns command
selection, bounded output, exit status, and delivery overflow. Stock process
execution and continuation retain the authority described below.

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
keeps the result and the Codex-owned process lifecycle unchanged, the remote
write guard of
[REQ-EXECUTION-003](#req-execution-003--guard-remote-vcs-writes), which can fail one
guarded command, and
[sudo authentication](#req-execution-004--unlock-sudo-through-the-native-dialog).
Dash-backed sh tracking, the enabled guard and sudo support use Codex's native
`PreToolUse.updatedInput` hook to instrument shell command text. This is a narrow
exception to byte-identical command input: tool identity, all other arguments,
selected shell, results, permissions, sandbox and continuations stay Codex-owned.
`exec` batching, including `Promise.allSettled`, remains available. Codex
owns permissions, sandboxing, command processes, yielded sessions, and
`write_stdin` continuation. Mekugi never reruns a stock call while observing,
replaying, or displaying it.

The [Codex backend](../../backends/codex/SPEC.md#execution-evidence) owns execution
evidence decoding shared by live continuation advice and offline inspection.

Completed edit observations may append a separate
agent-visible change-ID and summary text part as specified in
[applied changes](changes.md), preserving the original host result content.

Native command approvals retain Codex's offered session, command-prefix and
permission scopes. In the UI, command denial uses native decline so
the turn continues without running the command. The original native tool
rejection remains intact, with no synthetic shell exit code. Companion user
feedback reports denial with or without the optional reason in the existing
provider continuation for the same thread, turn and tool roundtrip; it neither
steers the host nor starts a new turn. File-change and permission approval
choices remain unchanged. Passthrough preserves the original host decisions
and does not offer typed denial reasons. Dock editing and
draft preservation are specified under [native UI approvals](native_ui.md#approvals).

The [frontend host](../../apps/mekugi/internal/frontends/SPEC.md) owns authenticated
executable exposure, worker identity, private PATH, and worker cleanup under
Codex's execution authority.

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
6. Executable frontends retain the Codex-owned executor, with no alternate tool
   carrier or MCP layer; the [frontend host](../../apps/mekugi/internal/frontends/SPEC.md)
   owns catalog and worker acceptance.
7. Native command denial preserves the host tool rejection and delivers optional
   user feedback only to the matching provider continuation, without a synthetic
   exit code, steering or a new turn. Host-offered approval scopes, file-change
   and permission choices remain available; proxy-free clients retain original
   host decisions without reason editing.

### Duplicate-output projection

The [output-deduplication module](../../modules/outputdeduplication/SPEC.md) owns
model-visible references, eligibility, and repetition receipts. Host effects and
original evidence remain unchanged.

## REQ-EXECUTION-002 — Track each segment of a command list

In the UI, a supported Bash command or Linux `/bin/sh` command backed by dash
whose script is a top-level list (`;`, newline, `&&`, `||`) reports each segment's
own output, exit status,
start/end timestamps and elapsed duration, so Activity can show every segment with
its own state and stream live output. Clicking a segment opens only its retained
stdout and stderr in the shared output dialog. Supported single commands,
including pipelines, report their own elapsed duration without redirecting
stdout or stderr. Bash also supports timed single commands. Conditions and branch
commands in unredirected, non-negated `if` blocks
report separate segments, so a completed formatter does not wait for tests in
the same branch. Pipelines and other compound commands are single segments.
Scripts with shell setup, such as standalone variable
assignments, declarations, shell options, sourced files, or function definitions,
stay one event with their full source and combined output. Recognized file-edit
operations keep their Edit rows and hidden edit bodies. Command-local environment
assignments and setup in subprocesses or pipelines keep existing segment boundaries.
Untrackable setup, including traps, keeps the host's aggregate event.
Authored command text, stdin, output bytes, exit status,
PTY/yield behavior, and `write_stdin` continuation stay those of the stock
command. Codex's startup, cancellation, sandbox, and process group apply
unchanged.

For Bash, the launcher adds a hook to the startup file that the frontend PATH
already uses. Tracking starts only in a host command's execution environment;
hook-handler shells do not start command matching or wait for its deadline.
Nested launches discard inherited host command identity before starting Codex,
which supplies the new command's identity at execution time.
Segment tracking requires Bash 4.1 or newer; older Bash runs the
original command once with aggregate host output. The hook runs in the command
shell Codex started, after login startup, so profile functions and aliases remain
available. It never evaluates
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

Closing tracking cancels its report connections and waits for its handlers to
release their private resources. It does not cancel host commands.

For dash-backed sh, Codex's native `PreToolUse` command hook adds inline
instrumentation to supported POSIX scripts. It runs in Codex's selected shell,
without launching a substitute interpreter, and uses the same segment reports,
measured timing, output retention and UI as Bash. Bash keeps its startup path.
The native hook is registered for tracking even with `--shell-guard=false`. Its
status message, part of its invocation-local trust identity, describes it in hook
rows: `Applying shell guard` with the guard enabled, otherwise `Tracking shell segments`.
With shell guarding disabled, explicit caller `PreToolUse` conflicts leave caller
settings intact and sh untracked. Enabled guarding replaces incompatible hooks
for this invocation after warning, as described below.
Missing or incompatible tracking resources leave commands running once
with aggregate host output. This support is limited to Linux dash-backed sh;
other sh implementations are not promised segment tracking.

Accepted tracking in Bash and sh also defines a `mktemp` shell function, unless
the shell already has one. It runs the utility once and keeps its output,
diagnostics, and status, then reports the existing file or directory it named,
including from command substitutions, subshells, and pipelines. Reporting can
neither fail nor change the command. Bash also reports `mktemp` invoked by an
absolute path in `PATH`, `/bin`, or `/usr/bin`, and, when the script text names
both `command` and `mktemp`, through `command`. Only then does Bash wrap
`command`, because the wrapper moves the line number in diagnostics from other
commands run through it. Dash cannot name functions by path or see the script,
so tracked sh reports bare `mktemp` only. `mktemp` run by another program is not
reported. Reported paths have their own bound, separate from the command-output
budget.

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
invocation duration. Protocol version 4 carries this timing with boundary reports
and a final closure record after all relayed output has drained.
Output a background descendant writes later is attributed to the segment that
is running when it arrives. A terminal command is not relayed, because
programs would detect a pipe; it reports statuses only and keeps the host's
combined output. The helper never blocks on the router. When reporting falls
behind or exceeds 4 MiB, it stops reporting output and continues relaying
unchanged.

Command-start matching observes received host notifications before they wait
for terminal presentation. A queued start does not lose its segment report
merely because presentation is delayed. Missing or late host
notifications still retain the untracked fallback; observed file changes are
not durable change receipts until the host result and capture are retained.

A completed command shows its segments only when its report reached final closure
with the host's own exit status. Otherwise, including a report that ends when the shell
replaces itself or is killed, Activity falls back to the host's combined result.
Completed, host-validated reports are retained by
[command segments](../../modules/commandsegments/SPEC.md) in the managed store,
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
remain separate; the [notice contract](../../modules/notifications/SPEC.md) owns access to full details,
dismissal, delivery and launcher recovery.

The same matching, host-exit validation, retention and history restoration apply
to single-command reports. Old history without reports, missing or mismatched
records, and incomplete reports keep the host timing and combined result.
Single-command reports retain host output unchanged. Terminal and lossy reports
retain their observed segment statuses but not separate output. If any segment's bounded
output was truncated or released before persistence, restored output likewise
stays combined rather than presenting a partial stream as complete. Activity and
output dialogs hide literal host shell wrappers and generated VCS and sudo guard
instrumentation in live and restored main/child activity, preserving user PATH
assignments and other shell source. This also applies with the VCS guard disabled.
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
   the current session's enabled VCS and sudo guards. Item-bound instrumentation must match the exact command item.
   Helper wrappers, unrecognized source, and other-session source stay intact;
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

Owned by [remote-write approval](../../modules/remotewriteapproval/SPEC.md).

## REQ-EXECUTION-004 — Unlock sudo through the native dialog

Owned by [sudo approval](../../modules/sudoapproval/SPEC.md).
