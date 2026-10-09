# mekugi

A mekugi is the small peg that pins a Japanese sword's handle to the blade.
Take it out and the handle comes off. Leave it in and the blade is still the
blade.

Mekugi adds compact, recoverable tools and live subagent activity to stock
Codex. Codex keeps editing, execution, the sandbox, permissions, command
sessions, and patch review. No fork, no config edits, no daemon. The
[Claude backend preview](#claude-code-preview) uses the official runtime in the same UI.

[Features](#features) · [Install](#install) · [Usage](#usage) ·
[UI](#ui) · [Metrics](#metrics) · [Configuration](#configuration) ·
[Documentation](#documentation)

## Features

- **Claude Code preview.** `mekugi claude` offers streaming conversation, native
  permission decisions, shared live/saved Diff, live command output with supported
  Bash command segments, retaining `!command` input, saved-session title/resume/clear
  controls, independent reached-command remote-write approval, Bash utilities and
  durable journals in the existing interface, without an inference router. See its
  [current limits and build instructions](#claude-code-preview).

### Agent-facing

- **Fewer tokens and round trips.** [Session helpers](#wrapped-session-helpers)
  offer bounded and batched reads, semantic symbol lookup, and structural outlines.
- **Recoverable output and changes.** Continue omitted output without rerunning,
  and review, revert, or reapply captured edits by ID. Gaps in coverage are labeled.
- **Leaner instructions.** [Omit marked instruction blocks](#troubleshooting-and-integrations)
  for the session without changing instruction files or unmarked policy.
- **Duplicate-output references.** Opt in with `--duplicate-output` to replace
  unchanged runs in tool output and attached file or skill content with references
  to earlier visible text in Mekugi mode. Full host results, retained evidence,
  and the UI stay intact;
  see the [projection contract](doc/spec/execution.md#duplicate-output-projection).
- **Task journal.** Record plans, results, constraints, and blockers as durable
  work state, rather than repeating status summaries in conversation.
- **Journal context reset.** Manual and context-full resets use retained journal,
  change and failure evidence by default, including subagents, without a provider
  summary request. Opt out with `--journal-compaction=off`; see
  [recovery behavior and limits](#troubleshooting-and-integrations).
- **Session continuity.** Retained journal and change evidence survives resume,
  forks, side threads, and compaction, subject to [storage retention](#replay-storage).
- **Custom tools.** Extend the agent's capabilities with your own
  [JavaScript tool plugins](doc/spec/plugin.md).

### User-facing

- **Auto resume accidental stop.** Continue unfinished journal work when the agent
  stops, including after answering a follow-up. Explicit user stops, blockers, and
  pending questions are respected. See the [Journal pane](#journal-pane).
- **Native terminal UI.** [Main, Diff, Activity, Journal, and Agents](#ui)
  share one terminal without an external pane manager.
- **Task journal.** Plans, results, and blockers stay visible without extra model
  requests for status reports. Work established complete by the final tool result
  needs no separate model-generated completion acknowledgment.
  See the [Journal pane](#journal-pane).
- **Live subagent activity.** See model, effort, progress, and message excerpts
  in Main or the [Agents pane](#agents-pane), alongside elapsed time and edit activity.
- **Live diffs.** [Inspect streaming previews and saved edits](#live-diff-pane);
  click an Edit event to open its captured file and hunk.
- **Readable command output.** Open [searchable retained output](#output-dialog).
  With `mekugi-exec`, supported Bash and Linux dash-backed sh command lists show
  each command's streamed output, exit status, and timing.
  Markdown file links open local source in the same dialog.
  Supported single commands and pipelines, including Bash timed commands, show measured
  command time excluding startup and matching.
- **File, directory and skill attachments.** [Send selected contents or a bounded directory tree directly](#composer)
  without a separate agent read; unreadable or oversized attachments have explicit notices.
- **Composer additions.** Mention selected messages or diffs, and ask
  [`/btw` side questions](#composer) without interrupting Main.
- **Usage, throughput and cost.** See per-thread usage and estimated API costs
  in Agents, with output tokens/sec in Main's composer and child-agent rows,
  plus launch-wide average throughput, usage
  coverage, retries, and diagnostics in [`/session`](#metrics).
- **Other providers and tiers.** Use [Grok](#grok-models) or
  [OpenCode Go and Zen](#opencode-go-and-zen), and choose [service tiers](#mekugi-settings).
- **Diagnostics.** [Inspect past sessions](#inspect-a-session), record debug evidence,
  or [replay the UI offline](#replay-a-session) with CPU and heap profiling.

Bounded output and leaner instructions aim to reduce tokens and round trips;
these savings don't guarantee better results on every task. For controlled
comparisons, use [codex-setup-ab](https://github.com/yusing/codex-setup-ab).

## Install

Requirements:

- **Codex CLI** for Codex and Grok [launch modes](#start-mekugi).
- For `mekugi claude`: **Node.js 18+** as `node` and the installed, authenticated official **Claude Code CLI** as `claude`. No Codex installation is needed for this backend.
- For configured JavaScript plugins only: **Node.js 24+** as `node`. Plugins declaring regex grammars also require **ripgrep** as `rg` on the router's `PATH`.
- Built-in frontends need neither Node.js nor Bun; semantic lookup requires the language servers listed under [agent-facing tools](#agent-facing).
- Any interpreter your agent picks, such as `python3`, on the executor's `PATH`.

### Release binaries

Download an archive from [GitHub Releases](https://github.com/yusing/mekugi/releases/latest).
Each archive includes `mekugi`, `mekugi-exec`, and the private `claude-bridge` runtime
bundle; Go, npm, and a C toolchain are not needed. Keep the bridge directory beside
`mekugi`, including when relocating the installation.

| Platform | Archive | Minimum OS |
| --- | --- | --- |
| Linux x86-64 | `mekugi_linux_amd64.tar.gz` | glibc 2.39, such as Ubuntu 24.04 |
| Linux ARM64 | `mekugi_linux_arm64.tar.gz` | glibc 2.39, such as Ubuntu 24.04 |
| macOS Apple Silicon | `mekugi_darwin_arm64.tar.gz` | macOS 15 |

For example, install the Linux x86-64 release into `~/.local/bin`:

```sh
(
  set -eu
  mekugi_install_tmp=$(mktemp -d)
  trap 'rm -rf "$mekugi_install_tmp"' EXIT
  cd "$mekugi_install_tmp"
  curl -fLO https://github.com/yusing/mekugi/releases/latest/download/mekugi_linux_amd64.tar.gz
  curl -fLO https://github.com/yusing/mekugi/releases/latest/download/SHA256SUMS
  sha256sum --ignore-missing -c SHA256SUMS
  tar -xzf mekugi_linux_amd64.tar.gz
  mkdir -p "$HOME/.local/bin"
  install -m 755 mekugi mekugi-exec "$HOME/.local/bin/"
  rm -rf "$HOME/.local/bin/claude-bridge"
  cp -R claude-bridge "$HOME/.local/bin/"
)
```

Use the archive for your platform. On macOS, verify it with
`shasum -a 256 --ignore-missing -c SHA256SUMS` instead of `sha256sum`.
Add `~/.local/bin` to your `PATH` if needed. Installing replaces both binaries
and their bridge bundle at that destination. Linux releases need glibc and do not
run on stock Alpine Linux; use a source build for other environments.

### Build from source

Source installs require **Go 1.27+**, CGO enabled, and a C toolchain.
A binary-only Go install supports Codex and Grok, but does not include the Claude bridge:

```sh
go install github.com/yusing/mekugi/cmd/mekugi@latest github.com/yusing/mekugi/cmd/mekugi-exec@latest
```

Add `$GOBIN`, or `$(go env GOPATH)/bin` if that is unset, to your `PATH`.

For all backends, use a checkout with **Make**, **Node.js 18+**, and **npm**:

```sh
git clone https://github.com/yusing/mekugi.git
cd mekugi
make install
```

This regenerates the optional plugin shared core, builds the locked Claude bridge,
and installs it beside `mekugi` and `mekugi-exec` in the Go binary directory.
`make uninstall` removes those binaries and their bridge bundle. Running sessions
keep their worker executable, so start a new session to pick up an update.

## Usage

### Claude Code preview

After a release or checkout installation, launch the Claude backend in the existing UI:

```sh
mekugi claude --cwd /path/to/project
```

For a development preview without replacing installed binaries:

```sh
make build-claude
./bin/mekugi claude --cwd /path/to/project
```

Requires Node.js 18 or newer and an installed, authenticated official `claude`
CLI. The bridge uses the SDK version locked by the checkout or release.
Sign in using Claude's own CLI before launching. Authentication and
billing remain native; this preview does not claim subscription eligibility or
switch you to API billing. Existing Claude settings, instructions and extensions
load normally, including their configured permission mode. Mekugi supplies its
shared utilities, journal MCP tools and observational hooks for this launch only.
It does not route inference or change persistent Claude settings.

The preview writes only to `bin/`, not the installed Mekugi location. Its private
production SDK dependencies stay in `bin/claude-bridge`; no global npm installation
is needed. Keep that directory beside `bin/mekugi`. The bridge uses your installed
Claude executable, not a second bundled Claude CLI. Launch works from any directory,
including through a symlink to `mekugi`. You can select a native model with
`--model MODEL` or continue a native session with `--resume SESSION_ID`; the ID appears at the bottom of the
screen. Resume preserves Claude's context and restores available transcript rows
from the selected workspace. Display restoration reads at most 2,000 messages;
a notice identifies longer histories. No tools or approvals are replayed.

Use `/title <title>` to save a native session title without a model request.
The shared `/resume` picker lists saved native sessions, newest first, with
paginated title, branch, directory and session-ID search. Tab toggles Cwd/All
and restarts a failed listing; Enter opens the selected session.
`/resume SESSION_ID` switches directly. All lists other workspaces; opening a
session moves the UI, Journal, Diff and utility frontends to its verified native
workspace. Launch-time `--resume SESSION_ID` also selects that workspace, even
when launched from another directory.

`/clear` starts empty context in the current workspace without copying the old
journal or change scope. Saved sessions and filesystem changes are not deleted.
Resume restores the selected session's native history/title and available retained
Mekugi evidence. Switching requires idle native work: active turns, settings,
permissions, Bash jobs, child agents and unfinished observations block it.
Validation failure keeps the old view; a command that cannot be sent keeps its
draft. If replacement fails after the old query has closed, resume manually
rather than expecting the old conversation to remain active.

Enter sends a message; Ctrl-J adds a line; bracketed paste preserves multiline
text. The same composer supports undo/redo, external editing and copying. Use
PgUp/PgDn or the mouse wheel to scroll, and Ctrl-B 1 / 2 / 3 to select Main / Diff /
Activity. Native approvals use the shared question dock: select Allow once or
Deny and press Enter. Questions accept option numbers or free text; multi-select
questions use Space to toggle choices before Enter. Ctrl-C declines a pending request,
otherwise clears the draft, interrupts active work, then quits when idle.
`/quit` exits when idle. `/lock` and `/unlock` control keyboard interruption.
`/live`, `/live on` and `/live off` control the shared live-edit dock. Pane layout
preferences are restored when resuming the same session. Input entered during a turn stays in the composer rather
than being silently steered or queued.

Type `!command` to use the shared Shell Mode composer. Claude executes it once,
without inference, sandboxing, per-command permission prompts or persistent shell
state. Remove attachments and picker tokens before submitting. Confirmed command
and output become context for later messages and resume; output appears after
execution, rather than streaming. Click its output row to open the shared dialog.
Cancelling uses native shutdown and resumes the same saved session. It also ends
other work in that Main query, while keeping the unsent draft and independent
side conversation. If shutdown cannot confirm completion, resume manually after
the reported failure. This shortcut follows native user-shell semantics rather
than the model's Bash tool permission policy.

`/model` opens the shared picker with Claude's advertised models; `/model MODEL`
uses the native model setter, including custom model IDs. `/effort` (or `/reasoning`)
shows the current model's advertised effort levels. `/effort default` resets the
session override to Claude's model default. Shift-Up/Down steps a requested effort;
without an override it opens the picker. Native acknowledgements and failures
settle these controls. Model switches affect subsequent native requests; effort
overrides apply on the next turn and may be limited by native policy. The composer
labels effort as a request, not a guaranteed effective level. These controls are
invocation-local and do not save settings files or offer Codex service tiers.

`/btw QUESTION` opens the shared side-question dock from a native snapshot of
Main. Answers stream without interrupting Main or changing its history. Repeat
`/btw` for a follow-up; later Main turns stay out of this side conversation.
File-picker attachments and pasted images are accepted, but the context-only side
query cannot use tools. PgUp/PgDn scroll; Esc closes only the side query and keeps
Main's draft. The temporary dock is not saved or restored on resume.

To branch a native conversation in a new launch, use
`mekugi claude --cwd /path/to/workspace --resume SESSION_ID --fork-session`.
Claude assigns a new session ID and retains the source conversation's context;
subsequent fork input does not change the source. Workspace admission and native
history remain the same as resume. Saved Diff captures remain scoped to their
native session: a fork does not copy the parent's capture history. Completed
command-segment reports follow inherited native history without reviving tools.

`/status`, `/usage` or `/session` opens the shared status dialog with the latest available
Claude-reported cumulative tokens, cache usage and limit windows. Missing fields
are omitted. API-equivalent cost estimates are labeled separately from subscription
charges; these views do not make token-count requests or show router traffic.
Main and native child agents appear in the shared Agents roster. Child conversation
text is forwarded into the shared Activity pane while native work continues.
Ctrl-B 4 focuses Agents; j/k select a child and `a` toggles its Activity filter.
Roster selection changes the view; ordinary composer input still goes to Main.
Use `/to <agent> <message>` to send a plain-text message directly to a child.
Type `/to ` to autocomplete roster targets, then Tab or Enter to insert one.
You can use its native ID or `/root/ID`. Busy children queue the message; finished
children continue their saved native context. Delivery confirmation means queued,
not completed. Claude records the sender as the invocation-local Mekugi message
plugin. Native permission decisions still apply. Failed sends keep the draft;
images and file-picker attachments need an ordinary Main or side-question input.
Background Bash jobs stay in the conversation as task progress, not extra agents.
Select a live child in Agents and press `x` to request its native stop; only a terminal runtime
event settles its row. Root-turn completion does not complete background tasks.
Tool calls use the same compact operation rows as Codex. Supported Bash command
lists show each command's actual exit or skipped status and its own output in the
shared rows and dialog. Click command-output or task-event rows in Main or Activity
to open it. Unsupported shell wrappers or scripts, and identical concurrent inputs
that cannot be attributed safely, run unchanged with combined output instead.
Completed per-command reports restore on native resume and forks, including
saved child rows, without repeating tools. Forks retain the child history selected
at branching; later source-child work stays out. Inherited child display requires
the original native child transcript; unavailable history is reported.

Unsegmented foreground and background commands update the dialog with native
last-8-KiB tail snapshots, not a lossless output stream; the dialog labels this
limitation. Native task registration has a two-second grace period, so short
commands may show only their final aggregate. The final native foreground result
remains authoritative. Background completion replaces the tail with the native
output file's complete UTF-8 aggregate and saves it for resume. Command dialogs
keep their normal display bounds; when bytes or lines are omitted, the dialog
shows an `mread` reference for the complete saved output. It remains attached to
that command after later activity and resume, without repeating execution.
Unreadable output or storage failure preserves the available tail and reports
the missing complete evidence.
Capture warnings remain
readable in the transcript when they do not fit in the status line.

Claude uses the existing pane shell, not a separate interface. Edit/Write input
streams and supported literal Bash heredoc/interpreter writes appear as provisional
proposals in the shared live-edit dock and Diff pane. Partial
content does not claim deletion of an unseen suffix. Child proposals follow the
selected Activity lane; the native runtime exposes complete child input rather
than partial child streams. Native tool completion does not turn proposals into
saved edit evidence. Query replacement removes live proposals; resume and forks
restore confirmed output and segments without restoring proposal workers.

Invocation-local observational hooks record actual Edit/Write
file effects, including partial effects of failed tools, enter the same saved Diff
pane and separate Activity capture cards with retained change IDs. The hooks leave
native arguments, results and permissions unchanged. They install no persistent settings. Bash observation uses bounded private workspace
snapshots; the native hook does not establish a shell executable, so literal Bash
operand coverage and ignored Bash targets are unavailable. Explicit Edit/Write
paths are captured even when ignored. Background captures remain unfinished until
a matching native terminal task event is available. Missing baselines, interrupted
observation or storage failures do not become successful saved changes. Resume
restores saved observations, never running hooks or processes. An unfinished
observation cannot attribute changes made while Mekugi was disconnected. Press
`v` in Diff to select live proposals or saved captures; your selection survives
updates. Permission prompts follow Claude's configured policy, including native
automatic approval; observation does not change that policy.

Interactive Claude adds a separate approval before a reached remote VCS write.
Allow it once, allow the exact command and workdir for this UI session, or deny
only that command. Skipped branches do not ask. The guard currently requires
native Bash selected through `CLAUDE_CODE_SHELL` or `SHELL`. The guard rejects
unsupported shell selection or conflicting native hooks. Settings that replace
the startup observer are rejected before Bash effects instead of being overwritten. Use
`mekugi claude --vcs-guard=false` to retain native permissions without this guard.
Native sandboxing and shell-prefix wrappers currently require that opt-out.
Unsupported or ambiguous command tracking still asks through the same approval
dock without assigning the decision to a guessed command row. Missing guard
resources or an incomplete startup handoff reject execution with an explanation.
Direct `!command` keeps its native user-action semantics.

The shared composer completes SDK-advertised commands and skills with `/` and
workspace file paths with `@`, including ignored files but excluding VCS internals.
File mentions remain native `@path` input; Claude resolves them through its own
workflow, potentially using native Read, rather than Mekugi submitting file contents. Select an image from
file completion, paste an image path, or use Ctrl-V for a clipboard image. Images
are sent as native content blocks, limited to PNG, JPEG, GIF or WebP, 5 MiB each
and an 8 MiB combined bridge frame. Failed submissions keep the draft and images.

Shared utility frontends run in native Bash, including
`mcat`, `inspect_file`, `msymbol`, `mrun`, `mread` and `mchanges`. An invocation-local
skill supplies mandatory journal workflow guidance, and the complete authenticated
utility contracts are injected automatically into native context. Fresh sessions
receive them through one native engine attachment; resumed sessions receive
current guidance without replacing the saved prompt. Children and compaction
receive the same guidance. Classic compact hooks add only changing journal facts.
The catalog file remains a recovery reference, not a
required extra read. Disabled native hooks, safe/bare modes, or unavailable hook
policy stop query startup with an explanation, without changing those settings.
Native managed-only mod restrictions also stop startup. The same option in user
or project settings does not impose administrative policy.
Completed foreground captures
return explicit change IDs through companion hook context, without replacing the
native tool result. Review those IDs with `mchanges`; apply/revert run only through
native Bash and its permissions, and their actual effects become new captures.
`mread` retrieves retained output without repeating execution. Bash does not expose
a trustworthy per-agent caller binding, so its `mchanges` frontend requires explicit
change IDs. The read-only native MCP `mchanges` tool supports empty arguments,
`--mine` and implicit `--list` using the authenticated caller's own retained changes.
Neither interface can select another workspace; apply/revert remain native Bash
operations with explicit IDs.

Native MCP journal batches/reads feed the shared Journal pane (`Ctrl-B 5`),
Main event cards and plan strip, without an additional activation flag.
Claude receives instructions to plan substantial work in the journal and keep
task states current. Large journal reads return a saved-output continuation for
`mread`, rather than failing when the tree exceeds one tool response. Its immutable
snapshot uses the same managed output chunks and continuation chain as other
retained reads, without a separate journal reader.
Journal calls require native tool identity matched to authenticated hook evidence,
not a model-provided agent ID. Native agent tool restrictions still apply;
agents without access to the companion MCP tools cannot author a journal.
Child journals mount read-only only after native
results establish ancestry. Turn completion leaves authored task states unchanged
and preserves Claude's substantive answer. Resume restores retained journal state.
Plain `/compact` prepares a fresh native session from the bounded journal context,
without asking Claude to summarize the conversation. The source conversation remains
independently resumable. The new session becomes resumable after its first input;
before that, the UI reports context preparation, not a completed reset.
Completed journal slices use the same reset. Other runnable journal tasks continue
after a three-second countdown without resetting context. Drafts and questions cancel
the countdown; Escape or interruption stops automatic continuation. Native background
work delays dispatch, and uncertain dispatch is never replayed after restart.

`/compact` with extra instructions still uses native summarization followed by bounded
additive journal recovery. Mandatory constraints and open tasks must fit the recovery
packet in full. Overflow or missing evidence leaves the native summary intact,
reports unavailable recovery facts, and still supplies current workflow guidance.
The utility and journal integration is invocation-local.

Native terminal foreground switching is unavailable through the direct-message
carrier; roster filters and `/to` provide shared viewing and child messaging. Command
segments and session controls do not imply full shared-controller parity.
Commands advertised by the SDK are forwarded natively;
unadvertised commands are rejected rather than emulated.
Command observation leaves native command input, setup, permissions, execution
and working-directory updates unchanged; it does not rewrite native results.
Only exposed text, native tool input/results and task/usage observations are displayed;
unavailable usage and change evidence are not invented. Oversized bridge events stop the client with an error;
Claude's own session remains the history authority. The full Codex interface below
is unchanged and is not a claim of Claude feature parity.

### Start Mekugi

Choose a mode and authenticate with the provider it uses:

| Launch | Models | Authentication |
| --- | --- | --- |
| `mekugi` | Authenticated third-party providers, using `grok:` and `opencode*:` IDs | [Grok credentials](#grok-models) or [OpenCode API keys](#opencode-go-and-zen); no Codex login |
| `mekugi grok` | Grok only, using plain IDs such as `grok-4.7` | [Grok credentials](#grok-models); no Codex login |
| `mekugi codex` | Codex only, no third-party models | `codex login` with ChatGPT authentication |

Standalone mode fails at startup if no third-party provider has credentials.
Its default uses [Grok's model selection](#grok-models) when authenticated,
otherwise the first available OpenCode Go model, then Zen.

Mekugi opens its terminal workspace. Codex's configured approval and
sandbox policy applies, and Mekugi also asks before remote repository writes; see
[Approvals](#approvals). Add `--yolo` to disable Codex approvals and sandboxing;
Mekugi's remote-write guard stays enabled. The guard requires unsandboxed command
execution: configure `sandbox_mode="danger-full-access"` or use `--yolo`.
Use `--vcs-guard=false` to disable the guard while retaining Codex's configured
approval and sandbox policy.

Mekugi flags go **before** `codex`, `grok`, or standalone Codex arguments.
Interactive launches accept `--yolo`, model
and config options, and [`resume`, `resume THREAD_ID` or `resume --last`](#resume); enter
prompts in the [UI](#ui). Noninteractive commands keep their
ordinary Codex arguments and output:

```sh
mekugi exec "Explain this repository"
```

### Launch behavior and limits

Independent sessions can run side by side. Ctrl-C during startup cancels the
launch. On UI exit, Mekugi prints commands to resume with the original
options or replay offline. Noninteractive commands retain Codex's exit status.

- There is no standalone router or daemon. Fixed ports, custom provider endpoints,
  and `--oss` are not supported.
- OpenAI sessions need secure WebSockets to ChatGPT. Mekugi falls back to
  HTTP only when ChatGPT explicitly rejects the upgrade, and never silently
  replays a request.
- Grok and OpenCode requests use HTTP and their own credentials. Third-party
  credentials do not enable those providers in `mekugi codex`.
- Mekugi-mode sessions require a model with Codex's JavaScript `exec` interface. Mekugi forces
  that interface for the invocation; unsupported models fail before inference.
- Invocation-only overrides disable the plan tool and collaboration-mode
  instructions, route `gpt-5.6-terra` to `gpt-6-sol`, and select standard cybersecurity safeguards
  rather than Daybreak. No configuration files change.
- Private [replay records](#replay-storage) include tool inputs and change
  evidence. Native Codex tracing also records prompts and responses in a
  temporary directory with no disk cap; it is removed at shutdown, but a
  forced kill can leave it behind.

### Approvals

Without `--yolo`, Codex's command, edit, and permission approval requests open a
dialog above Main's composer with Codex's offered approval scopes, including
session, command-prefix, or permission grants when available. It opens by
itself only over an empty composer; otherwise a banner waits until you press
`Ctrl-B`, then `q`. Number keys choose while the reason is empty; arrows choose,
`Enter` confirms, and `Esc`
hides the dialog without answering.

For command approvals in Mekugi mode, typing or pasting selects denial and edits
an optional reason; `Enter` confirms. Your composer draft and attachments stay
parked and return when the dialog closes. Hiding and reopening keeps the reason.
Questions and approvals share the dock, so only one is open at a time.
The model receives the native command rejection and your feedback in the current
turn, without a new prompt or turn. Edit and permission choices stay unchanged.
Proxy-free direct UI and passthrough retain Codex's original decisions without
typed denial reasons.

In interactive UI launches, Mekugi asks before remote repository writes reached
through guarded shell commands by default, independently of Codex's approval
policy, including with `--yolo`. Disable only this guard with
`mekugi --vcs-guard=false codex` or `mekugi --vcs-guard=false` for standalone
mode. Command tracking remains enabled. Headless and noninteractive commands
have no guard. Guarded writes include:

- `git push` to any remote, including tag and mirror pushes, plus `git send-email`,
  `git svn dcommit`, `git p4 submit`, and Git LFS pushes and locks
- `gh` commands that change GitHub state; unrecognized `gh` commands count as writes
- `hg push` and `hg email`; `svn commit`, `import`, `lock`, and commands that change
  a repository URL directly, such as `svn copy ^/trunk ^/tags/v1`
- `jj git push` and `jj gerrit upload`; unrecognized `jj` commands, including
  aliases, count as writes

Git aliases, `git submodule foreach`, `rebase --exec`, and `bisect run` scripts
are checked before they run. Read-only commands such as `git fetch` or `gh pr view`
run without asking.

The guard offers approval once, approval for this exact command and workdir for
the UI session, or denial with an optional typed or pasted reason. Session
approval matches the expanded argument list, workdir, and resolved executable;
it also releases identical requests already waiting. It grants no command-prefix
permission and expires when the UI closes, including before a resumed launch.

If you deny it, don't answer within 5 minutes, or the approval connection is
blocked or unreachable, only that command fails, with exit status 1; the shell
continues as it would after any failure. A user denial reports your supplied
reason, or that no reason was given, on stderr. The denied command never runs.
This shell exit status belongs to the guard; native Codex approval denial remains
a tool rejection.
If the command stops while waiting,
its request is withdrawn. For example,
`git add -A; git commit -m msg; git push origin main; git log` still runs
`git log`, while the same list joined with `&&` stops at the push.

Git subcommands that are neither built in nor aliases, such as `git subtree push`
or a third-party `git-*` command, also ask, since they can push out of the guard's
sight. A tool whose own write runs another guarded tool, such as `gh pr create`
pushing your branch, asks again for that inner command.

The guard needs the default `--mode mekugi`, `mekugi-exec` installed beside
`mekugi`, Codex's native command-hook support, and unsandboxed command execution
(`sandbox_mode="danger-full-access"` or `--yolo`). Sandboxed execution is
unsupported. Mekugi never silently
changes Codex's approval or sandbox policy. It keeps Codex's selected
shell, including Bash, sh and zsh. Direct commands and supported wrappers such
as `env`, `command`, `exec`, `xargs` and `timeout` are guarded by name or by
absolute or relative executable path, including paths with spaces and expanded
paths such as `"$tools/git"`. Nested shell `-c` commands are checked after their
payload expands. Shell arguments, sandbox permissions and command sessions
remain Codex-owned.

Before each new user turn, Mekugi verifies that the guard hook is enabled and
trusted. A missing guard or a competing trusted synchronous shell hook blocks
the turn with an explanation and preserves your draft. Explicit CLI overrides
of `hooks` or `hooks.PreToolUse` conflict with the enabled guard and prevent launch.
User configuration files are unchanged.

This is protection against accidental remote writes, not a process sandbox.
It does not comprehensively intercept executable words computed entirely at
runtime, `eval`, `env -S` payloads, sourced files, script files, or arbitrary
programs that internally execute absolute VCS paths. Bash/zsh startup guards
provide additional PATH and known-absolute-path coverage inside scripts, but
these limits still apply. Hook failures outside Mekugi's handler follow Codex's
failure policy and need not block execution. See the
[execution contract](doc/spec/execution.md#req-execution-003--guard-remote-vcs-writes).

### Headless slice plans

The `headless --yolo` subcommand reads one prompt from stdin and runs a new
session to completion, continuing unfinished journal work and any planned slices:

```sh
mekugi --journal-compaction=slice headless --yolo < prompt.txt
```

The default `auto` resets at slice boundaries and for manual or context-full
requests. Use `off` to continue slices in one context with provider compaction,
or `slice` to reset only between slices and use provider compaction otherwise.
Unlike the UI, headless journal continuations have no countdown delay.
It accepts model and `-c` options, not resume or positional prompts. Prompts must
be nonempty and at most 16 MiB. Unlike the UI, it requires explicit `--yolo`.
It does not answer approval or user-input questions: those end the run with an
error rather than hanging or guessing. Ctrl-C stops the run.

Stdout is JSONL containing host app-server messages and namespaced Mekugi reset
and completion events. Diagnostics go to stderr. The event stream includes prompt,
tool and answer content; it is session evidence, not sanitized metrics. A nonzero
exit means the run or its shutdown failed. Ordinary `codex exec` is unchanged.

### Options

| Flag | Default | Purpose |
| --- | --- | --- |
| `--ansi-faint` | `auto` | Dimming: `auto` detects mosh ancestry, `on` uses ANSI faint, `off` uses fixed muted colors |
| `--mode` | `mekugi` | Use `passthrough` with `mekugi codex` to forward traffic without Mekugi tools or plugins |
| `--vcs-guard` | `true` | Ask before remote VCS writes in the UI, even with `--yolo`; `false` disables only this guard |
| `--post-compact-recovery` | `true` | Use `false` to skip the post-compaction context hook |
| `--journal-compaction` | `auto` | Journal context reset without a provider request; `off` restores provider compaction; `slice` resets only between planned slices |
| `--duplicate-output` | `false` | Opt in to duplicate-output and attachment references in model input; full results and evidence stay intact; no effect in passthrough |
| `--grok-auth-file` | `~/.grok/auth.json` | Select a Grok OAuth credential store |
| `--timeout` | `10m` | Wait for the upstream response to start |
| `--stream-idle-timeout` | `4m` | Limit gaps between provider messages during an active response, or HTTP response bytes |
| `--capture-output PATH` | Disabled | Append sanitized JSONL metrics |
| `--debug` | Disabled | Record diagnostics, capture, metrics, forwarded instruction/tool snapshots, runtime reads, and an AX report; print a session diagnosis command on exit |

`mekugi --mode passthrough --vcs-guard=false codex` forwards traffic only. It doesn't
need Node.js, and capture still works. Interactive passthrough can't
[guard remote writes](#approvals), so it requires `--vcs-guard=false` and retains
Codex's configured approval and sandbox policy. Passthrough implies
`--journal-compaction=off`; explicit `auto` or `slice` is rejected.

If a detached multiplexer hides your mosh connection, use
`mekugi --ansi-faint=off codex` for readable dimmed text. The setting applies only
to that invocation and does not modify terminal configuration.

### Grok models

Authenticate with `grok login --oauth`, or set `XAI_API_KEY` in the router's
environment; an API key takes precedence. Codex credentials are never sent to Grok.

```sh
mekugi grok -m grok-4.7
mekugi -m grok:grok-4.7
```

The default follows `[models].default` in `~/.grok/config.toml`, falling back to
the latest standard model, currently `grok-4.7`. An explicit `-m` or `-c model=...`
takes precedence. Standalone mode adds the `grok:` prefix to this default;
`mekugi grok` uses plain IDs. The old `--grok` flag is not supported.

Subagents use the same IDs as their launch mode: `grok-4.5`, `grok-4.6`,
`grok-4.7`, or `grok-4.7-build-fast`, prefixed with `grok:` in standalone mode.
Use fresh context (`fork_turns="none"`). Build Fast requires Grok OAuth, not an API key.
Grok can't read encrypted OpenAI history, so switching an existing OpenAI
conversation to Grok isn't supported. See the [Grok requirements](doc/spec/grok.md).

### OpenCode Go and Zen

Set an API key here or in [Mekugi settings](#mekugi-settings):

```sh
OPENCODE_GO_API_KEY='your-key' mekugi -m opencode-go:glm-5.3
OPENCODE_ZEN_API_KEY='your-key' mekugi -m opencode-zen:kimi-k3
```

Models, reasoning controls, and prices refresh from an hourly cache. The model
picker updates on the next launch. See the [provider contract](doc/spec/opencode.md).

`OPENCODE_API_KEY` overrides both service keys from the settings file. Per-service
variables override it and any file keys; an empty per-service value disables that service.

## UI

This section describes the Codex-backed interface. Claude uses the same pane
shell with the capabilities and limits in [Claude Code preview](#claude-code-preview).

Every interactive launch lays out Main, Diff, Activity, Journal,
and Agents panes in one terminal without an external pane manager. Main holds the
conversation and composer. Inline Markdown code uses content-detected syntax
colors; unrecognized spans stay plain with an accent. Fenced code uses its
language tag. Markdown tables render as aligned grids that switch
to a record layout in narrow panes. Completed Mermaid flowchart fences render as
terminal diagrams with labeled solid/dashed edges and `&` fan-in/fan-out groups.
Unsupported syntax (including subgraphs), incomplete fences, and diagrams too
wide for the pane remain readable source.

The empty launch view shows the Mekugi and
Codex versions. Release builds show their tag, such as `mekugi-v1.2.3`, rather than
a commit SHA. Click tool output, long-content excerpts, or edit rows to open
the shared scrollable dialog without leaving your current view. Close it with
`Esc` or its top-right close button.

- `Ctrl-B`, then `1`/`2`/`3`/`4`/`5`, focuses Main, Diff, Activity, Agents, or Journal.
  Diff, Activity and Journal share the right column. Click a pane or its bottom
  selector to focus it.
  New sessions show Journal there. A live child spawn or follow-up temporarily
  shows Activity; when all children finish, Journal returns unless you selected
  another pane. Saved pane preferences still apply on resume.
- Drag the dividers to resize panes or the file navigator. `Ctrl-B`, then arrow
  keys, resizes the main splits (up/down in the roster adjusts its height);
  `Ctrl-B`, then `[`/`]`, resizes the file navigator. Narrow terminals show the
  focused pane full-width.
- `Ctrl-B`, then `PageUp`/`PageDown`, browses Main history. The wheel scrolls
  the pane under the pointer.
- `Ctrl-C` in an auxiliary pane returns focus to Main.

Main's top-right header shows the session title. Use `/title <title>` to rename
it, even before the session has started or while a task is running. The new title
appears immediately and is saved with the session; a save failure restores the
last saved title and shows composer feedback. Manual renaming skips automatic
title generation, including after resume or fork.
For a new conversation without a title,
the first successful upstream request starts a separate title-generation request
using GPT-6 Luna with medium reasoning. This small additional model request does
not block the conversation. If Codex credentials or Luna are unavailable, automatic
naming is silently skipped, without switching to another model. Saved and inherited
titles are reused on resume or fork.

Terminal titles and desktop notifications follow Codex's
`tui.notifications` settings. Inside Herdr, its working, blocked, done, and
idle indicators update even with notifications off.

See the [UI contract](doc/spec/native_ui.md).

### Composer

Use input editing, file and skill pickers, steering, and
queuing: Enter steers a running turn; Tab queues input. Type `?` in an empty
composer for the full shortcut list. The
controls and attachment behavior are described below.

Use `/lock` to prevent Esc or Ctrl-C from interrupting work and to disable
Ctrl-C exit on an empty draft. `/unlock` restores those shortcuts. Draft clearing,
copying, and closing pickers still work while locked; `/quit` remains an explicit
exit when idle. A persistent Locked indicator shows the protection, which is
restored with the session on resume. It does not block explicit commands or
steering new input.

- **Files and directory trees attach.** `@!` also finds ignored files; neither picker lists
  VCS metadata such as `.git`. Selected text files attach their contents when
  you submit or queue the prompt, and large or unreadable files produce an
  explicit omission notice instead of truncated content. Directories attach a
  sorted tree of children and grandchildren, not file contents. Trees respect
  ancestor and nested `.gitignore` files, exclude VCS metadata, and list at most
  256 entries within 16 KiB. A scan limit also bounds large directories; incomplete
  trees have an explicit truncation marker. Queued prompts retain the submitted tree.
- **Select to mention.** Drag across Main, Activity, or the saved Diff and press
  `r` to add the selection as one short mention, such as `[Selected message]` or
  `[Selected diff hunk @amber1:42-45]`. The selected text is sent with the
  prompt without filling the composer or losing its formatting.
- **Skill references attach automatically.** Complete enabled `$skill-name`
  references are recognized when you finish the word or submit a pasted prompt;
  unknown, disabled, or ambiguous names stay plain text. With `skills-mgr`, managed
  skills attach their actual instructions automatically, with an `Attached skill` receipt.
  Click successful file or skill attachment receipts to open their submitted contents,
  including after resume.
  Unreadable or oversized contents produce an explicit omission notice.
  Without it, contents come from Codex-discovered skill files. Use `skills-mgr`
  to manage its skills; `/skills` toggles Codex-discovered skills.
- **Waiting messages combine.** Steers typed while an earlier one is still
  sending, and queued messages, each appear as one stack and send as one message,
  one entry per line. Alt+Up or Shift+Left returns all locally waiting input to
  the composer. Already-sent steers remain pending until Codex commits them or
  the turn ends. While a turn is active, Esc expedites pending or locally waiting
  steers: it interrupts the turn, then sends only uncommitted steers in a new
  turn, leaving your draft unchanged. Tab-queued input stays queued when a steer
  is resent. If all pending steers commit before interruption completes, nothing
  is resent and queued input returns to the composer as in a normal interrupt.
  Already-committed input is never resent.
  Ctrl-C clears the draft first, then interrupts and restores waiting input
  without resending it. With no pending steers, Esc interrupts and restores input
  without resending, and never quits. Closing a picker, help, or selection and
  returning scrollback to the bottom take precedence over Esc interruption.
- **Session controls.** `/compact` performs a context reset by default. Enter interrupts
  the current turn and resets after Codex acknowledges its end, with or without
  `instant_interrupt`. Tab queues the reset until the current turn ends. After a
  successful busy reset, waiting
  input runs next; without waiting input, Mekugi sends a visible continuation
  message to resume the task.
  An idle reset stays idle, and failure or interruption never automatically
  continues it. Ctrl+C cancels a queued reset and restores waiting input
  without interrupting Main. Cancelling while an Enter-command interruption is
  pending prevents the reset but cannot undo that interruption; once the reset
  starts, Ctrl+C interrupts only that reset. `/clear` starts a fresh session and
  clears its transcript; it is
  available while idle and does not delete saved sessions or filesystem changes.
  A normal interrupt returns unsent input to the composer without automatically
  resending it; Esc with pending steers uses the expedited delivery above.
  Interrupting an uncommitted first message leaves an empty transcript.
- **Live diffs.** Edits temporarily replace Main's transcript or the editing
  agent's Activity transcript, leaving other agents visible. Brief edits finish
  without opening this temporary view; their saved diffs remain available.
  Ctrl-B e cycles and pins files; Ctrl-B r resumes Main's live following. Completed
  edits stay together until the batch settles, then the transcript returns.
  `/live` toggles the view; `/live on` or `/live off` applies directly for the
  current session without stopping activity or capture. New launches, including
  resume, show it again.
- **Setting pickers.** `/model`, `/effort` (also `/reasoning`), and `/tier` open
  a picker: Up/Down and Enter apply a choice, Esc cancels, and neither the
  command nor its choices enter the transcript. A value switches directly, such
  as `/effort high` or `/tier priority`; `/tier default` clears the tier.
  Changes also reach a running turn's next steps and never edit your config file.
- **Terminal requirements.** `/copy` and text selection copy through OSC 52,
  which your terminal must allow. Pasting a clipboard image on Linux needs
  `wl-paste` (Wayland) or `xclip` (X11).

`/btw QUESTION` asks a side question about a snapshot of the conversation, even
while Main is working. The answer streams in a panel above the composer, with
shimmering Answering status and elapsed time. Drag across its content to copy or
reference it, just as in Main. Repeat
`/btw QUESTION` to follow up, use PgUp/PgDn to scroll, and press Esc to clear a
selection or close the panel;
Main keeps working and your draft stays. Closing discards the side
conversation, and it can't be resumed.
If the side question needs compaction, it is rejected and restored to the composer.
Run `/compact` on Main, close the side panel, and retry to take a fresh snapshot.

### Resume

`resume THREAD_ID` and `resume --last` work as in Codex. Startup resume and `/resume`
restore the session's saved model, reasoning effort, and service tier; explicit
`-m` and `-c` flags override only the settings they name. Resuming also restores
pane layout, keyboard focus, the agent roster, Activity history, and the
session's retained Diff changes. Scroll positions, filters, selections, and
drafts are not restored. Successfully applied settings survive a fresh launch,
including priority and default selections made before the first turn. Storage
failures are reported in Activity. If no saved model settings are available, a
notice explains that Codex defaults and explicit flags apply instead. Main
histories larger than 16 MiB cannot resume in the UI. Older child activity loads on demand.
In third-party modes, noninteractive `exec resume` and `exec fork` use the
mode's default model rather than the saved one; pass `-m` to choose another.
Codex does not save an empty conversation before its first turn.
If Codex rejects restoring default reasoning, waiting input returns to the
composer and stays blocked until you choose `/effort VALUE` or restart resume.

Bare `resume`, or `/resume` inside a session, opens a picker of saved sessions
for the current workspace, newest first. Type to search, Tab to show every
workspace, and Enter to resume. Esc clears the search, then starts a new session
at launch or closes the picker in a session; Ctrl-C quits at launch. `/resume`
switches the UI to the chosen session and is unavailable while a task runs;
`/resume THREAD_ID` switches directly.

### Live diff pane

The live diff viewer opens at the first edit or command; read-only turns don't
open it. Main-agent and subagent calls get labeled cards that stream input as it
arrives. When a turn finishes, the viewer switches to the saved diff. A failed
or unfinished call never becomes a saved change.

- `v` switches views; `?` lists diff shortcuts.
- In the saved Diff, `s` shows and focuses the file/Changes navigator; press
  it again to hide the navigator. `t` toggles tree/flat paths, `/` filters files.
- The navigator and diff have separate focus. Arrow keys act on the focused
  region. Selecting or clicking a file previews it while keeping list focus;
  `Enter` opens it and focuses the diff. Click the diff to focus it, or press
  `Esc` from an opened diff to return to the list.
- `n`/`p` change files, `[`/`]` jump between hunks, and `j`/`k`, `Space`/`b`,
  and `g`/`G` scroll. Opening a file starts at its header.
- `Tab` switches the navigator to **Changes**: a graph of changes by caller
  (`main` or the agent's name) with each change's source, such as
  `apply_patch`, `sed`, or `python3`. `{`/`}` step through changes, `a` shows
  one caller's changes at a time, and `0` shows all callers. In the tab, `Enter`
  on a caller filters to it, and `Enter` or `h`/`l` on a change expands or
  collapses its files.
- Click an Edit to inspect its captured diff in the shared dialog. Reply,
  question, and journal-agent links also open dialogs, leaving pane filters
  and scroll positions unchanged. Completed host file-change diffs open even
  while other commands in the same batch are still running. `Esc` dismisses
  the dialog.
- New saved edits refresh the content without moving your chosen file or
  scroll position. Streaming previews still follow live edits until you browse;
  `r` resumes preview following.

See [live view details](doc/spec/changes.md#live-terminal-view).

### Journal pane

Press `Ctrl-B` then `5` to open Journal. Its title counts tasks by state. Open tasks
come first; subtrees start expanded and collapse oldest-first when space runs short.
Use `j`/`k` or arrows to select,
`Space` to expand or collapse, `d` to read full details, `Enter` to open a row, and
`c` to copy the selected path. Click a `▸` marker to expand it, or click a row to open it.
`Esc` returns to Main. Blocked tasks show their reason; notes have no state label.
Entries replaced by later direction stay dimmed with `superseded by` and the path of
their replacement.
Delegated journals appear under their owning task, or in an Agents group after
the owned plan. Agent headings show their lifecycle separately from task states;
expanded descendants use local paths without repeating the agent name. Outcomes
follow the work they summarize. Press
`c` to copy their full journal address. Press `Enter` on, or click, an agent to
open its Activity.

The plan strip stays above the composer while work remains. Main offers a
continuation countdown after an agent stops with unfinished work, including after
answering a follow-up. Esc cancels. Blocked tasks and pending questions pause
automatic continuation. Explicitly stopped work stays paused across later questions,
forks, and resume until reactivated. Ordinary continuation keeps the current
context, while planned slice boundaries follow the selected
compaction mode. Headless sessions use the same policy without a countdown and
require interactive continuation when a question needs an answer.

Main shows task transitions and, while Journal is hidden, notes. Work updates appear in separate
cards; open a card for full evidence and older notes. Ordinary answers remain
in the conversation, and an unchanged plan does not add a report card. Task timers
count active work, pause while blocked or the agent is inactive, and retain their
totals when work resumes.

### Agents pane

The **Activity** pane streams child activity, messages, and replies, including
while Main waits. The **Agents** roster below the main columns shows children
with their active elapsed time, provider round trips, output tokens/sec,
and cumulative edited lines as
`+N -N`. The roster header's `+N -N` reports the final composed outcome
across agents, so superseded edits and files created then deleted do not inflate it.
It uses recorded changes, not a live Git diff; incomplete or inconsistent evidence
shows `?`. Main's conversation and progress stay in Main. Usage and estimated
API costs appear per thread and survive resume while their records are retained,
including for completed agents. Missing consumption is not zero: `≥` marks known
lower bounds (including restored totals), and unavailable metrics stay blank.
Older sessions without retained accounting cannot recover usage from context
counts. Elapsed timers exclude idle gaps and freeze when work stops; the separate
last-response age continues counting. Main's output throughput appears only beside
context in its composer, not in its roster row.
The last valid provider-measured rate stays visible while streaming and between
requests until a newer valid terminal measurement arrives. Throughput stays blank
before the first valid measurement. Visible streaming omits hidden reasoning, so
it is not used to estimate total output throughput. Measured rates survive resume.
See [Metrics](#metrics) for launch-wide totals.

- Click an agent to inspect its activity; reply links address that agent.
- In Activity or Agents, `a` toggles selected-agent filtering. The hint reads
  `a all` while filtered, and `a only` in the shared view.
- Scrolling pauses following; End resumes it. The mouse wheel scrolls without
  changing keyboard focus.
- When the Activity title shows `Older history`, focus Activity and press `o`
  to load an older page for the selected child, or the first child with older
  history in the shared view. Esc cancels the read; `o` retries a failed read.
  Intentionally unloaded history is distinct from unavailable evidence.

Redirected sessions keep ordinary Codex input/output and inline agent activity.
Inside Herdr, Mekugi advertises the invocation through Herdr's agent hint.
Herdr is optional and does not control Mekugi's internal panes.

### Output dialog

Click a command, program, read, error, or long-content excerpt in Main or Activity to
open its retained content in the shared dialog above the panes, without expanding
the transcript. Read source and unified diffs use syntax colors. Untyped command
output above 8 KiB stays uncolored for responsiveness; its text remains available
for reading, searching, and copying. File-specific and diff highlighting retain
their 256 KiB limit. Supported Bash commands and Linux `/bin/sh` commands backed
by dash use the same command tracking. Each segment of a command list streams
its own output and shows its state and measured elapsed suffix; clicking it opens
that segment's tab with its retained output and exit status in the shared dialog.
Supported single commands use measured command time in the same suffix, leaving
output unchanged.
See [command tracking](doc/spec/execution.md) for limits and overhead.
Older history without timing evidence shows no per-command duration.
Without retained output boundaries, the dialog labels the output as combined.
Errors keep a short inline preview. Click **details**, or focus Main or Activity
and press `Ctrl-B` then `!`, to read the full error, including restored
JavaScript execution failures. The keyboard shortcut opens the newest error; Left/Right reaches the
other retained errors in that transcript. Host-omitted text cannot be recovered.

Click a Markdown link to an existing local file in Main, Activity, or a dialog
to read its current contents. Relative paths resolve against the session workspace;
absolute paths and local `file:///` links also work. A `:line` suffix scrolls to
that source line when it exists. The path row uses workspace-relative display
inside the workspace and stays absolute outside it. Drag to select the path or
visible content, then `y`, `c`, or Ctrl-C copies it; `y` without a selection copies
the original file contents.
Files must be UTF-8 text no larger than 8 MiB. Read failures, oversized files,
and binary/non-UTF-8 files show red, copyable errors. Terminal controls are
sanitized for display. Reopening reads the file again; an open dialog does not
monitor changes. HTTP(S) links still copy their destinations in Main and Activity;
clicking them inside a dialog still does nothing. Missing or unrecognized file
links keep their prior behavior.

- Left/Right or a click switches tabs. `j`/`k`, `PgUp`/`PgDn`, and `g`/`G`
  scroll; live output follows its tail until you scroll up.
- `/` searches, and `n`/`N` step through matches.
- `y` copies the page's output. Drag to select text, then `y`, `c`, or Ctrl-C
  copies the selection.
- The top-right `[×]` button, `Esc`, `q`, or a click outside closes it.

See [activity display](doc/spec/activity_display.md).

## Recoverable output and change review

Saved diffs and `mchanges` record recognized source edits, including edits to
ignored files, and the file changes of any other command that may write, such as
a Python script, found by comparing workspace snapshots taken before and after
it. Agents do not need `apply_patch` to have their edits recorded. Inside a Git
repository, ignored paths such as an ignored `node_modules/` stay out of the
record unless an edit names them; outside Git, bulk new files in one directory
are left out. Changes made by you while a command runs are attributed to that
command. LiveDiff previews remain provisional.

### Wrapped-session helpers

Session helpers let the agent read less output and recover what it omitted,
or review and undo recorded edits. They are available only inside a wrapped
session; you can ask the agent to use them.

| Command | Purpose | Requirements |
| --- | --- | --- |
| `mread` | Continue bounded retained output by reference | Access to the router's replay directory |
| `mrun` | Bound a foreground command's output and optionally keep its ending | The wrapped command |
| `mchanges` | Review the current thread's edits, compose captured diffs, or read/revert/reapply explicit IDs | Access to the router's replay directory |
| `mcat` | Read raw UTF-8 rows, with multi-file batching, ranges, and tail selection | None |
| `msymbol` | Look up compact definitions and file-grouped references; batch queries in one call | `gopls` for Go; TypeScript 7 as `tsc` for JS, TS, and JSON; `pyright-langserver` for Python |
| `inspect_file` | Inspect a structural outline | None |

```sh
mchanges --list
mchanges amber1..amber3 --summary
mchanges revert amber2
mread REF
mcat --number source.ts 10-20 40:60
inspect_file source.ts
mrun --tail -n 20 go test ./internal/router
```

`mchanges` is the recorded view of agent changes, including ordinary Git-ignored
files such as `FIXME.md`, not a live Git diff. Dependency trees are summarized as
one directory status, such as `M node_modules/`, without descendant listings or
content. Manifest and lockfile changes remain reviewable. Directory summaries
cannot be reverted or reapplied because their contents are not retained.
Tests, generators, and other commands without known targets also record changes
observed while they ran. These are labeled `observed during command window`,
since another writer may have made them; unavailable evidence is reported rather
than treated as no changes. Like
`git revert`, `mchanges revert` merges any later edits and marks overlaps with
conflict markers. The revert is recorded as a new change, so it can be undone
too. See the [change record](doc/spec/changes.md), [reader](doc/spec/read.md),
and [execution contract](doc/spec/execution.md).

## Metrics

Open `/session` in Main for the current launch's request and retry counts, provider usage,
usage-evidence coverage, average output tokens/sec, measured cache rate, capture
health, and transport details. Average throughput is measured output tokens divided
by the sum of their provider-request seconds, including request latency and automatic
transport retries, not an average of individual rates or session wall time. Local
preparation, tool execution, and idle time between requests are excluded. Measured
rates use provider-response receipt time, before local processing
and delivery of already-read output. Unread transport buffering and backpressure
still count, so these rates describe observed provider-request throughput, not
provider-only generation speed. Requests without timing or authoritative token totals
are excluded from both sums; measured
request count and duration show the coverage. Output includes provider-counted
reasoning tokens. A measured zero-output request has zero throughput; missing timing
is unavailable.
Exchanges shows timings, errors, and each attempt's provider-returned
service tier, not the configured tier; missing evidence stays unavailable.
Left/Right or Tab switches Overview, Transport, and Exchanges;
Up/Down, mouse wheel, and PgUp/PgDn scroll. In Exchanges, `[` selects older and
`]` newer observations. `r` refreshes immediately; the open dialog also refreshes
once per second. Escape closes without interrupting a turn. Metrics include all
threads in this launch, not just the currently displayed session, and do not
restore historical totals after resume. There is no browser dashboard.

The same sanitized metrics remain available through the local API:

```sh
curl -sS "${MEKUGI_BASE_URL%/v1}/api/metrics"
mekugi --capture-output capture.jsonl codex
```

Launch metrics stay in memory unless captured with `--capture-output`;
captures hold sanitized measurements only, with no prompts, patches, or
credentials. Provider-reported usage is authoritative; local token estimates
are not billing figures. Costs are API estimates, not subscription charges. See
the [metrics reference](doc/spec/metrics.md).

`mekugi --debug codex` writes a private `mekugi-debug-*` bundle under
`$XDG_STATE_HOME/mekugi/debug` (default `~/.local/state/mekugi/debug`).
Inactive bundles are removed after 14 days; copy a bundle elsewhere to keep it.
Explicit external capture/read destinations are not removed. Existing temporary
bundles are left untouched. `/session` shows the bundle path and measured
application-write bytes in Overview; final metrics include the same accounting.
These bytes cover this router's managed-record and debug/capture writes, not
physical disk traffic or subprocess writes. On exit,
“To diagnose this session” prints an inspection command ready to share with an
agent. Debug bundles include private session content and are **not sanitized**.
Repeated instruction/tool content is stored once and referenced by later requests,
while request and wire-projection evidence remain available through the inspection
command. Its report includes artifact sizes and application-write metrics to help
identify bulky or incomplete evidence. Older bundles remain readable and are not
rewritten. Debug mode records future requests only. See
[debug evidence](doc/spec/router.md#feature-usage-debug-evidence).

## Configuration

### Mekugi settings

Create `mekugi/config.toml` in your user configuration directory:
`$XDG_CONFIG_HOME` or `~/.config` on Linux, `~/Library/Application Support` on macOS.

```toml
# Optional overrides, keyed by exact model ID.
[service_tiers]
"gpt-6-astra" = "fast"
"gpt-5.6-sol" = "default"

[providers.opencode_go]
api_key = "your-go-key"

[providers.opencode_zen]
api_key = "your-zen-key"
```

Every section is optional. Settings are read at startup and never rewritten.

- **Service tiers** replace the request's tier after model selection. The values
  are `auto`, `default`, `fast` (sent as
  `priority`), `priority`, and `flex`. The provider must support the tier you
  choose. The composer and `/status` show the effective tier, including
  this override, rather than only Codex's requested tier.
  `/tier` shows the effective current tier. Confirmed explicit choices, including
  `default`, override configured defaults for that thread and routed model during
  this invocation without changing your config file.
  `/session` reports the provider-returned tier, which can differ
  from the requested tier.
- **API keys:** these file keys are optional alternatives to the
  [OpenCode environment variables](#opencode-go-and-zen).

### Troubleshooting and integrations

- **Post-compaction recovery:** After compaction, Mekugi restores a bounded
  snapshot of Main's journal and changes. Subagents and passthrough mode are
  unaffected. This needs Codex's compact `SessionStart` hook support (tested with
  CLI 0.156.1). Opt out with `--post-compact-recovery=false` or `/hooks`.
  If Codex asks you to review the hook, use `/hooks`. Custom `-c hooks=...`
  settings are left alone; to enable recovery with them, add a `SessionStart`
  handler matching `^compact$` that runs `/absolute/path/to/mekugi post-compact`.
  Hook failures don't stop the task. See [guidance behavior](doc/spec/guide.md).
- **Journal context reset:** The default `--journal-compaction=auto` uses retained
  task, constraint, change and failed-command evidence for `/compact` and
  context-full requests instead of asking the provider for a summary, including
  child threads. Unavailable or ambiguous identity, evidence, storage or recovery
  rendering stops the reset with a visible error and no provider request.
  Recovery keeps constraints and open work inline; completed task bodies and agent
  history stay available through journal reads when needed. This does not establish
  improved model success or token savings.
  Opt out with `--journal-compaction=off` for provider compaction. `slice` uses journal
  resets only at planned slice boundaries and provider compaction otherwise.
  When the latest host-reported context use reaches at least 70% of the model
  context window, the next model request reminds the agent to split work into slices.
  Ordinary unfinished-work continuation keeps context. At planned slice boundaries,
  `auto` and `slice` reset before continuing, while `off` continues without resetting.
  The UI offers a countdown; Esc cancels. An interrupted reset may require manual
  continuation. Auto-mode commands and progress say **Context reset**. Exact retained
  journal-answer evidence adds **Context reset from journal**; click it to read the
  recovery message shown to the model. Older or expired messages are marked
  unavailable. Generic reset rows do not claim journal provenance or zero provider
  tokens. See [reset behavior](doc/spec/journal.md#router-answered-compaction).
- **Instructions:** Wrapped sessions disable Codex's `/goal` feature. Anything
  between `<!-- mekugi:omit -->` and `<!-- /mekugi:omit -->` in instruction files
  is omitted for the session; the files themselves are not changed. When
  `skills-mgr` is on the `PATH`, Mekugi turns off Codex's stock skill catalog
  for the session. Managed `$skill` selections attach their
  instructions in the composer; other Codex-selected skill injections
  remain compact name references. See [guidance behavior](doc/spec/guide.md).
- **Issue reports:** start with `MEKUGI_DIAGNOSE=1` to give agents a
  `report_issue` tool. Each report runs the commands in `hooks.diagnose` of
  `mekugi/settings.json` in your user configuration directory. Commands are
  templates with `.Title`, `.Body`, `shellquote`, and `format_markdown`, for
  example `gh issue create --title {{shellquote .Title}} --body {{shellquote .Body}}`.
  See [agent issue reports](doc/spec/diagnose.md).
- **Plugins:** put `.js` or `.mjs` modules in `mekugi/plugins` in your user
  configuration directory. See the [plugin contract](doc/spec/plugin.md).
- **Executor environment:** the router and executor must see the same workspace
  paths and runtime directory. `MEKUGI_RUNTIME_DIR` overrides the default
  temporary directory.
- **Failures:** startup errors print before Codex launches. Session failures
  appear as user-only commentary; undelivered notices print to stderr after
  exit. A router translation fault ends the turn and suggests recovery steps.

### Replay storage

Replay records live in `$XDG_STATE_HOME/mekugi/replay`, or
`~/.local/state/mekugi/replay` if that variable is unset. Resuming and side
conversations need no extra flag. Replay doesn't rerun old commands or restore
processes. Roster usage shares this managed store; existing records need no
migration. Publication remains atomic for readers, with disk flushing left to the
kernel. Sudden power loss may lose recent records; unavailable retained evidence
is not recovered by rerunning the original operation.

For read-only storage diagnosis, use `mekugi inspect-storage`; see the
[storage inspection reference](doc/spec/router.md) for selectors and output limits.

Mekugi retains up to **4 GiB** of managed session data and deletes it after
**14 days without activity**. When storage is
full, it removes the least recently active inactive sessions first, and never
touches running work. **Your Codex chats and workspace files are never deleted.**
If storage is full, Mekugi reports when evidence cannot be retained. Free space
and retry retaining the evidence, not the completed command or edit.
Cleanup can break old recovery and review references. To reset, stop all Mekugi
wrappers and move the directory aside.

### Inspect a session

This reads a Codex rollout without running anything or starting a router:

```sh
mekugi inspect-session --session /path/to/rollout.jsonl
mekugi inspect-session --debug-dir /path/to/debug --field diagnostic
mekugi inspect-session --failures
mekugi inspect-sessions --exclude-model '*grok*' --class production
```

The default JSON holds tool names, call IDs, outcomes, and sizes, but no
private text. For a reported issue with a debug bundle, use the printed
`--debug-dir` command to see failure evidence, capture health, metrics, and AX results.
Use `--request-id ID --field all` to drill into one request's capture and instruction
evidence. Values you request with `--field` may include private error text, instructions,
source, and command output. See [session inspection](doc/spec/session.md) and
[AX evidence](doc/spec/ax.md).

### Replay a session

Review a retained session through the UI without running Codex,
calling providers, executing commands, or answering questions:

```sh
mekugi replay-session --session SESSION_ID
mekugi replay-session --session SESSION_ID --debug-dir /path/to/debug --speed 4
```

Use the full session ID, not a rollout path. Mekugi finds the session in
`sessions` or `archived_sessions` under `CODEX_HOME` (default `~/.codex`). Missing
or duplicate rollouts are reported as errors.

Playback defaults to **1.0x**. Streaming is simulated from retained text and
timing, not a screen recording; `--seed 1` makes comparisons repeatable.
Referenced child rollouts are included when available. Retained journal records
restore task progress, delegated hierarchies, and completion cards when timing is available. Missing
records are reported. Original keystrokes, window sizes, and live diff previews
are not reconstructed.

The playback bar shows position, speed, and simulated-streaming status. **p**
pauses, **+/-** steps through speed presets from 0.1x to 100x, **[/]** seeks ten
seconds, **r** restarts, **Ctrl-B 1–5** selects Main, Diff, Activity, Agents, or
Journal, and **q** quits. In Journal, **j/k** or arrows select, **Space** expands
or collapses, **d/Enter** opens read-only details, and the wheel scrolls the tree.
**Esc/q** closes details before quitting replay. Outside Journal, **Space** also
pauses and **j/k** or the wheel scrolls Main.
Playback stops on its final frame until you quit.
Use `--from 5m --until 6m` to review a recorded interval; earlier state is loaded
first. The current terminal size controls layout.

For offline measurements, use `--headless --width 160 --height 48`; stdout is a
JSON timing summary. Headless playback renders the UI but cannot measure terminal
backpressure. Compare runs with the same inputs, seed, speed, and dimensions;
use 1.0x for representative latency. Use the [profiling build](#profile-live-sessions-and-replay)
to collect profiles from live sessions or replay. Session contents remain local
and may appear on screen. See the [replay reference](doc/spec/session_replay.md).

### Older installations

Finish active sessions before replacing an older installation. Retire any old
service and provider configuration separately, keeping unrelated settings and
authentication. Then choose a current [launch mode](#start-mekugi).

## Documentation

- [Product specification](doc/product.md)
- [Interface contracts](doc/spec/index.md)
- [Architecture ownership](doc/architecture/index.md)
- [Controlled comparisons](doc/benchmarks.md)
- [Codex end-to-end checks](doc/codex-router-e2e.md)

## Development

### Profile live sessions and replay

From a checkout with the [source-build prerequisites](#build-from-source), run
`make mekugi-pprof`. It regenerates the existing preview assets and builds
`bin/mekugi-pprof` with optimized code, symbols, and the `pprof` build tag, plus
its executable sibling `bin/mekugi-exec`. It does not install or replace `mekugi`.
Use the diagnostic binary with your usual launch arguments, or replay offline:

```sh
bin/mekugi-pprof codex --yolo
# Alternatively:
bin/mekugi-pprof replay-session --session SESSION_ID --headless --width 160 --height 48 --seed 1 --speed 1
```

Before the UI takes over, stderr prints an invocation-specific URL:
`mekugi-pprof: http://127.0.0.1:PORT/debug/pprof/`.
While that session or replay is running, copy its URL into a second terminal:

```sh
umask 077
MEKUGI_PPROF_URL='http://127.0.0.1:PORT/debug/pprof/'
go tool pprof -seconds 30 bin/mekugi-pprof "${MEKUGI_PPROF_URL}profile"
go tool pprof bin/mekugi-pprof "${MEKUGI_PPROF_URL}heap"
```

Replace `PORT` with the printed port. The index also offers allocs, goroutine,
block, mutex, and execution-trace endpoints. CPU and trace collection start only
on request; block sampling uses a 1 ms blocked-time rate and mutex sampling records
one in ten contention events. Instrumentation can perturb performance. Profiles
cover Mekugi's router, UI, and replay process; separate Codex, executor, and worker
processes need their own measurements.

The listener is private IPv4 loopback, with no additional authentication. Local
callers can read command-line arguments and other private process data; keep
captures private. Session or replay completion ends active captures and closes
the listener. The normal build starts no profiling listener, and replay creates
no automatic profile files. See the [profiling contract](doc/spec/router.md#performance-profiling)
and [replay comparison limits](#replay-a-session).

### Build and test

The [build workflow](.github/workflows/release.yml) tests and packages both commands
for Linux amd64/arm64 and macOS arm64 on pushes to `main`, pull requests, and manual
runs. Pushing a `v*` tag also publishes the three archives and `SHA256SUMS` to a
GitHub release, with the tag embedded as the welcome version. Rerunning a tag build
replaces that release's matching assets.

The workflow also checks the Claude bridge offline and includes its private SDK
dependencies beside both commands. Extracted and relocated Claude startup is
smoke-tested without model requests on every build platform.

Before packaging, Linux amd64 runs the fresh full offline Go suite and `make lint`
with pinned lint tools. Other platforms run command and welcome/version checks.

To review the UI without Codex or model requests, run
`make preview-native-ui` in a terminal. It plays a synthetic session through the
real panes and renderer: streaming and long messages, journal edits, retraction
and flush, agent summaries, and Main/agent communication. Scroll, resize, and
click reply links to inspect them. Keys behave as in the real UI: Enter steers
the playing turn or, once idle, starts a new one that echoes your prompt; Tab
queues for the next turn; Ctrl-C clears the draft, then interrupts playback,
then exits, as does `/quit`.

Run `make test-claude` for offline bridge compilation and transport tests. After
`make build-claude`, run `make test-claude-package CLAUDE_PACKAGE_DIR=bin` to
check extracted and relocated package startup with a fake Claude executable.
These checks do not prove compatibility with an authenticated live Claude session.
The opt-in [Claude acceptance checks](CONTEXT-TESTS.md#focused-checks) cover native
guidance delivery, ordinary journal adoption, command segments and session lifecycle.
Shared title/list/clear/resume acceptance uses the installed native runtime with
a scripted provider, rendered picker and no inference. Cross-workspace acceptance
also exercises the shared PTY picker's All filter, search and Enter in both
directions, plus fresh launch from another workspace. Full session-control PTY
acceptance covers title, paging/search, clear/resume, failures and cancellation;
live-model acceptance remains separate.
Native segment fixtures check shared UI events, with separate narrow/wide renderer
snapshots and gated PTY checks for live segment dialogs, clicks and cancellation.
Retaining-shell API and shared PTY checks prove native execution, later context,
fresh resume, fork isolation, cancellation handoff and draft/model/effort continuity.
The API check also proves that an independent side conversation survives Main
shell cancellation and retains its follow-up context. These use a local provider
without inference; live-model behavior and background-task/permission cleanup
during shell handoff remain separate coverage gaps.
Prompt-delivery and command-output PTY fixtures use a scripted local provider without inference;
live adoption and other live lifecycle checks use the installed authenticated runtime with
its normal billing.

For offline terminal-layout regression checks, run `make test-ui-snapshots`.
Failures leave `.txt.new` candidates beside the reviewed fixtures and print a
diff without replacing the baseline. After reviewing an intentional change,
run `make update-ui-snapshots SNAPSHOT='^TestUISnapshotJournalReply$'`, then
rerun the check. Omitting `SNAPSHOT` updates all matching cases. See
[terminal UI snapshot testing](CONTEXT-TESTS.md#terminal-ui-snapshots) for fixture
locations and coverage limits.

The full offline Go suite includes configured-plugin fixtures and requires
**Node.js 24+** and **ripgrep** on `PATH`, even when using built-in frontends.
`make lint` requires **golangci-lint** and **deadcode** on `PATH`; it does not
install them. It runs the [configured Go linters](.golangci.yml) and deadcode
analysis including test executables. Lint findings, deadcode findings, and tool
failures all fail the command.

```sh
make preview-assets
make test
make lint
```

Built-in frontends are native Go. Go regenerates the optional plugin shared core;
Bun is needed only for the separately invoked JavaScript plugin-host and
shared-core tests: `bun test ./internal/router/toolplugin/tests`.

While implementing, select the affected Go packages and tests, for example
`make test TEST_PACKAGES=./internal/router TEST_RUN='^TestShellRunnerMRun'`.
The default checks all packages and uses Go's test cache; add
`TEST_FLAGS=-count=1` for an uncached run. See [focused checks](CONTEXT-TESTS.md#focused-checks).

## License

MIT. See [LICENSE](LICENSE).
