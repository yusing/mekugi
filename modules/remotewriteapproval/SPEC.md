# Remote-write approval

## Purpose

Classify remote VCS writes and retain exact session approval grants.

## Scope

The composed interactive shell guard participates in the shared native hook.
Removing this module removes its instrumentation, accepted request kinds, and
session grants. The other approval module and command tracking remain independent.
No module registration exposes an agent-facing tool.

## Behavior

Interactive UI launches guard remote version-control writes by default,
independently of Codex's approval policy, including with `--yolo`. Writes wait
for the user's approval in the UI. This covers Git, GitHub CLI, Mercurial,
Subversion and Jujutsu, without restricting the remote, ref or tag being written.
The Mekugi flag `--shell-guard=false`, placed before `codex` or standalone Codex
arguments, disables both remote-write and sudo guarding, preserving Codex's
approval policy and independent command tracking. Headless and noninteractive
commands have no guard.

The guard requires unsandboxed command execution: configure
`sandbox_mode="danger-full-access"` or use `--yolo`. The latter disables Codex
approvals and sandboxing, not this guard. Sandboxed guard execution is unsupported.
Mekugi does not silently change the user's configuration, approval policy or
sandbox settings.

The guard uses Codex's synchronous native command hook to invoke the local
`mekugi-exec` helper and instrument shell command text before execution. It
requires no MCP endpoint and exposes no model-facing tool. It leaves expansion and control flow to Codex's selected
shell, including Bash and sh, rather than running the
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
suffix. Nested `sh`, `bash`, `dash` and `ksh` command-string invocations
are instrumented after their `-c` payload expands. Bare tool and shell names
retain shell-function lookup and use the command-local PATH; `command -p`
retains its default-path lookup. A caller-selected argument zero is forwarded
to the requested executable without selecting a different guard operation.

The enabled guard requires Mekugi mode, the installed `mekugi-exec` sibling,
Codex's native command-hook support and the approval channel. Before each new
user turn, the UI verifies the workspace's effective hooks. A missing, disabled
or untrusted guard, a changed hook identity, or a competing trusted synchronous
shell hook blocks the turn with an explanation and preserves the draft. At
startup, Mekugi queries Codex's effective hooks and session configuration in
the invocation workspace without starting a thread or model request. It warns
before disabling incompatible trusted synchronous shell hooks for this invocation,
then appends its composed guard. Existing groups and handler indices, unrelated
events, async observers, nonmatching handlers and their trust state remain intact.
Managed conflicting hooks cannot be replaced and block launch with an explanation.
User configuration files remain unchanged. Later workspace or configuration changes
are checked at turn admission; newly conflicting hooks block with the draft retained
rather than being replaced dynamically. Invocation-local hook setup preserves
recovery-hook trust.

Guard instrumentation failures reject the native tool with the actual failure
reason. The hook's shell adapter also converts missing or failing helper
processes into a native denial. Host hook-shell launch failures, timeouts, and
result-delivery failures still follow Codex's external-hook failure policy. Expanded hook events and their output dialog retain that reason.

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
  enables a dry run. For `push`, option values and refspecs after `--`
  cannot supply help or dry-run options. Also guarded: `subtree push`, `svn dcommit`,
  `p4 submit`, and `lfs push`, `lock` and `unlock`. Git global options are skipped. Another subcommand outside
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
programs that internally execute absolute VCS paths. Bash startup
guards add PATH and known-absolute-path coverage inside scripts without changing
user startup files; they do not remove these limits. Parsing failures reported
by Mekugi's hook reject that tool call. Hook failures outside the handler follow
Codex's host failure policy and need not fail closed.

An approved command that runs another guarded write, such as `gh pr create`
pushing a branch through a guarded Git lookup, asks again for the inner command.

## Lifecycle

The application creates one module-owned grant set for each attached UI lifetime.
Grants are invocation-local and discarded on detach; changing the displayed thread
does not create another set. Fresh resume creates a new set. The application owns
the shared local decision channel and joins it independently of segment reporting.

## Codex connection

The binding registers this guard's participation and its session-grant factory.
The application composes only registered resources into the backend's native hook.
The helper evaluates the module policy against expanded arguments at execution;
Git alias resolution uses the selected executable and its actual environment.
Host hook-shell launch and timeout policy remain Codex-owned. The helper does not
compose the application or duplicate host execution.

## Records

None. Grants and secret answers are never persisted.

## Failure

An unavailable decision channel cannot authorize a command. Requests for an absent
module are rejected before UI presentation. The remaining guard keeps its own
resources, including nested-shell instrumentation when only sudo is present.

## Acceptance
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
   The same holds in Bash and sh, by tool name and by direct or
   supported-wrapper executable path,
   including expanded paths and paths with spaces.
5. A nested shell command string is checked after expansion. Command-local PATH,
   shell functions, `command -p`, environment assignments and shell options
   retain their native lookup and expansion behavior.
6. A missing, disabled or untrusted effective guard, or a competing trusted
   synchronous shell hook, blocks a new user turn without losing the draft or
   modifying user configuration.
7. The default guard remains enabled with `--yolo`. Explicit `--shell-guard=false`
   removes remote-write and sudo prompts and instrumentation while preserving
   independent command tracking and the chosen
   Codex approval and sandbox policy.
8. Session approval releases exact queued duplicates and subsequent matching
   commands only within the same UI session. Changed arguments, workdir or
   resolved executable require another decision; closing and resuming clears
   the grant. Denial with or without a reason emits the specified stderr and
   exits 1 without running the real command.
9. Loss of an unrelated MCP endpoint does not bypass approval. A missing or
   failing local hook helper produces native denial before an absolute-path
   write executes. This excludes host hook-shell launch and timeout failures.

Module acceptance exercises policy and isolated grant lifetimes through the
scripted binder. Application acceptance covers leave-one-out resources, request
admission, exact queued grants, secret entry, and native execution. Installed-host
checks remain distinct from offline fixtures.
