# Shell instrumentation

## Purpose and scope

Generate supported guard and command-tracking source and recognize its exact
generated forms. Launch, the native hook, helper, capture and display share these
rules. Callers own execution, approval policy, authenticated invocation paths and
[report transport](../../modules/commandsegments/SPEC.md).

## Command boundaries

Supported Bash and Linux dash-backed sh scripts split top-level `;`, newline,
`&&` and `||` lists into source-ordered segments. Conditions and branches of
unredirected, non-negated `if` blocks split independently. Pipelines and other
compound commands remain single segments. Standalone assignments, declarations,
shell options, sourced files and function definitions keep the full script as one
event; command-local assignments and setup inside subprocesses or pipelines do
not merge its other boundaries. Bash also observes timed single commands.

Tracking declines lists beginning with a subshell or timed command, and source
using job control, traps, `coproc`, top-level `return`, process-replacing `exec`,
command tracing or instrumentation-sensitive shell variables. Unsupported syntax
and unrecognized source remain untracked rather than partially rewritten.

## Bash startup

Bash 4.1 or newer runs tracking in the host-started command shell after login
startup, retaining profile functions and aliases. Older Bash runs the original
command once with combined host output. Hook-handler shells never start matching.
Codex snapshot scripts and their wrappers pass through, leaving the inner command
shell eligible. Nested command shells stay untracked; nested Mekugi launches
discard inherited host command identity before Codex supplies a new one.

The installed `mekugi-exec` sibling uses the same splitter as report validation.
Only a unique live host match and acquired startup resources admit tracking.
Before admission, missing resources leave the original script to run once.
After admission, router cancellation cannot suppress execution; Codex still owns
command cancellation. Tracking uses existing FIFOs and router-created resources,
including under read-only and workspace-write sandboxes without network access
or helper-created filesystem resources.

A single command runs in place with begin/EXIT observations and unchanged output
descriptors. A list runs its instrumented copy once in the original shell, before
the first authored command. The rewrite preserves `$?`, `set -e`, shell state,
native errors, line numbers and fatal errors; it never starts a substitute shell.
Instrumentation-sensitive variables are screened before starting the helper.

## Dash-backed sh

The native `PreToolUse` hook inserts supported POSIX tracking source into the
selected shell. Tracking is independent of guard enablement; launch owns caller
hook conflict handling. Support is limited to Linux dash-backed sh. Scripts using
transport descriptors 7 or 8, computed descriptor duplicates, or inherited open
descriptors 3, 4, 7 or 8 remain untracked. Missing or incompatible resources leave
the command running once with combined host output.

## Observed temporary paths

Accepted Bash/sh tracking reports existing paths returned by `mktemp`, including
inside command substitutions, subshells and pipelines. It leaves an existing
shell function intact; otherwise it runs the utility once, preserving its output,
diagnostics and status. Failed, help, version and dry-run invocations establish
no creation. Reporting cannot change command success.

Bash also reports direct absolute `mktemp` paths found in PATH, `/bin` or
`/usr/bin` when the executable path contains only ASCII letters, digits and
`/._+-`. Other direct absolute paths execute unchanged without a creation report.
When the script names both `command` and `mktemp`, Bash also reports invocations
through `command`. Only then does it wrap `command`, because wrapping shifts
diagnostics for other commands. Dash reports bare `mktemp` only. Invocations from
another program remain outside coverage.
Path reports have a separate bound from command-output reporting; capture owns
their admission as observed filesystem targets.

## Guard and display source

Literal shell-command decoding is shared by observation and presentation. It
removes quoting without environment expansion or execution and rejects dynamic,
compound, redirected or extra-argument wrappers. Original command bytes remain
authoritative for execution and retention.

The library composes selected VCS and sudo guards without deciding approval.
The [remote-write](../../modules/remotewriteapproval/SPEC.md) and
[sudo](../../modules/sudoapproval/SPEC.md) modules own supported operations,
policy, grants and failure behavior. Generated source preserves native shell
lookup and expansion within those declared limits.

Display recognition removes only exact generated forms for caller-authenticated
guard paths. User PATH assignments, quoted source, heredocs and unrecognized
wrappers stay intact. Live and restored Main/child Activity can hide host shell
wrappers and generated guards without changing stored commands or execution,
including when the VCS guard is disabled. Recognition performs no filesystem
read or source evaluation. Preview matching uses the narrower exact identity
rules in [command segments](../../modules/commandsegments/SPEC.md#live-correlation).

## Acceptance

Tracked and untracked Bash/dash lists must preserve stdout, stderr and exit,
including `$?`, `cd`, `exit N`, `set -e` with `||` and `!`, heredocs,
short-circuiting, native errors and fatal expansion. Single commands, pipelines
and Bash timed commands keep original descriptors. Snapshot wrappers, nested
shells, unsupported source and missing resources run once. Terminal programs
keep their terminal. Guard recognition preserves authored source and cannot
authorize execution. Module/application fixtures and installed-Codex sandbox
checks remain distinct coverage.
