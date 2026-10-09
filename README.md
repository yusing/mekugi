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
- **Diagnostics.** [Inspect past sessions](docs/contributing/development.md#inspect-a-session), record debug evidence,
  or [replay the UI offline](docs/contributing/development.md#replay-a-session) with CPU and heap profiling.

Bounded output and leaner instructions aim to reduce tokens and round trips;
these savings don't guarantee better results on every task. For controlled
comparisons, use [codex-setup-ab](https://github.com/yusing/codex-setup-ab).

## Install

Requirements:

- **Codex CLI** for Codex and Grok [launch modes](#start-mekugi).
- For `mekugi claude`: **Node.js 18+** as `node` and the installed, authenticated official **Claude Code CLI** as `claude`.
- For configured JavaScript plugins only: **Node.js 24+** as `node`. Plugins declaring regex grammars also require **ripgrep** as `rg` on the router's `PATH`.
- Built-in frontends need neither Node.js nor Bun; semantic lookup requires the language servers listed under [agent-facing tools](#agent-facing).
- Any interpreter your agent picks, such as `python3`, on the executor's `PATH`.

### Release binaries

Download an archive from [GitHub Releases](https://github.com/yusing/mekugi/releases/latest).
Each archive includes `mekugi`, `mekugi-exec`, and the private `claude-bridge` bundle;
Go, npm, and a C toolchain are not needed. Keep the bundle beside `mekugi`.

| Platform | Archive | Minimum OS |
| --- | --- | --- |
| Linux x86-64 | `mekugi_linux_amd64.tar.gz` | glibc 2.39, such as Ubuntu 24.04 |
| Linux ARM64 | `mekugi_linux_arm64.tar.gz` | glibc 2.39, such as Ubuntu 24.04 |
| macOS Apple Silicon | `mekugi_darwin_arm64.tar.gz` | macOS 15 |

The [installer](install.sh) selects your platform, verifies the release checksum,
and installs both binaries and their bridge bundle into `~/.local/bin`, replacing existing copies:

```sh
curl -fsSL https://raw.githubusercontent.com/yusing/mekugi/main/install.sh | sh
```

Add `~/.local/bin` to your `PATH` if needed. Linux releases need glibc and do not
run on stock Alpine Linux; use a source build for other environments.

### Build from source

Source installs require **Go 1.27+**, CGO enabled, and a C toolchain.
The binary-only Go install supports Codex and Grok, without the Claude bridge:

```sh
go install github.com/yusing/mekugi/cmd/mekugi@latest github.com/yusing/mekugi/cmd/mekugi-exec@latest
```

Add `$GOBIN`, or `$(go env GOPATH)/bin` if that is unset, to your `PATH`.

From a checkout with **Make**, **Node.js 18+**, and **npm**, run `make install`.
It regenerates the optional plugin shared core and installs `mekugi`, `mekugi-exec`
and the locked Claude bridge. `make uninstall` removes the binaries and bundle. Running sessions keep their worker executable, so
start a new session to pick up an update.

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

| Launch | Models | Authentication |
| --- | --- | --- |
| `mekugi` | Third-party `grok:...` and `opencode*:` IDs | [Grok](#grok-models) or [OpenCode](#opencode-go-and-zen) credentials |
| `mekugi grok` | Plain Grok IDs, such as `grok-4.7` | Grok credentials |
| `mekugi codex` | Codex models only | `codex login` with ChatGPT authentication |
| `mekugi claude` | Native Claude models | Official Claude CLI authentication |

Standalone requires third-party credentials. Its default follows Grok's model selection
when authenticated, otherwise the first available OpenCode Go model, then Zen. Mekugi
flags go **before** `codex`, `grok`, or standalone Codex arguments.

Codex's approval/sandbox policy applies. The UI's [remote-write guard](#approvals) needs
unsandboxed execution: configure `sandbox_mode="danger-full-access"`, or use `--yolo` to
disable Codex approvals/sandboxing while keeping the guard. To disable only the guard
and keep Codex's configured policy, use `--vcs-guard=false`.

```sh
mekugi codex
mekugi grok -m grok-4.7
mekugi -m opencode-go:glm-5.3
mekugi exec "Explain this repository"
```

Interactive launches open the [UI](#ui); noninteractive commands retain Codex's
arguments/output/exit status. Ctrl-C cancels startup; exit prints resume/replay commands.
Mekugi mode needs JavaScript `exec`. There is no daemon, fixed/custom endpoint, or `--oss`.
See [launch and transport limits](doc/spec/router.md); invocation overrides leave config unchanged.

### Grok models

Use `grok login --oauth` or `XAI_API_KEY` (takes precedence); Codex credentials stay separate.
The default follows `[models].default` in `~/.grok/config.toml`, then the latest standard model (currently `grok-4.7`);
`-m`/`-c model=...` overrides it. Standalone IDs need `grok:`; `mekugi grok` uses plain IDs.
Subagents use fresh context (`fork_turns="none"`). Build Fast needs OAuth;
encrypted OpenAI history cannot switch to Grok. See [Grok requirements](doc/spec/grok.md).

### OpenCode Go and Zen

```sh
OPENCODE_GO_API_KEY='your-key' mekugi -m opencode-go:glm-5.3
OPENCODE_ZEN_API_KEY='your-key' mekugi -m opencode-zen:kimi-k3
```

Keys can also live in [settings](#mekugi-settings). `OPENCODE_API_KEY` overrides file
keys; per-service variables override it, and an empty value disables that service.
Models/prices refresh hourly; the picker updates next launch. See
[OpenCode](doc/spec/opencode.md).

### Resume

`resume THREAD_ID` or `resume --last` restores a session; bare `resume` opens the
workspace picker. Type to search, Tab for all workspaces, Enter to resume. Inside an
idle session, `/resume` opens the picker or `/resume THREAD_ID` switches directly. Saved
model/effort/tier, layout, roster, Activity, and retained Diff return; explicit
`-m`/`-c` overrides named settings. Drafts, selections, filters, and scroll positions do
not return. UI histories above 16 MiB cannot resume. Third-party `exec resume`/`exec
fork` uses the mode's default model unless given `-m`. See [resume
details](doc/spec/native_ui.md).

### Approvals

Codex approvals open over an empty composer; otherwise press `Ctrl-B`, then `q`. Arrows
choose, Enter confirms, Esc hides without answering. Typing/pasting in Mekugi-mode
command approvals selects denial with an optional reason; feedback reaches the current
turn. Your draft/attachments return afterward.

The guard asks before supported Git/GitHub/Mercurial/SVN/Jujutsu remote writes in
interactive Mekugi mode, even with `--yolo`; headless/noninteractive runs have no guard.
It needs `mekugi-exec` beside `mekugi`, native command hooks, and unsandboxed execution.
Approve once, approve the exact command/workdir/executable for this UI session, or deny.
Denial, a five-minute timeout, or an unreachable approval connection fails only that
command with status 1; the shell follows normal `;`/`&&` behavior. Session grants expire
when the UI closes. Missing/conflicting trusted guard hooks block turns; explicit hook
overrides conflict. Read-only commands run without asking.

Coverage is incomplete for runtime-computed commands, `eval`, scripts/sourced files, and
arbitrary programs invoking VCS internally. See [guard
limits](doc/spec/execution.md#req-execution-003--guard-remote-vcs-writes).

### Headless slice plans

```sh
mekugi --journal-compaction=slice headless --yolo < prompt.txt
```

Headless requires `--yolo` and one nonempty stdin prompt up to 16 MiB; model/`-c`
options are accepted, resume/positional prompts are not. It continues journal
work/slices without a countdown. Questions/approvals fail; Ctrl-C stops the run. Stdout
JSONL contains private prompt/tool/answer evidence and reset/completion events;
diagnostics go to stderr. Run/shutdown failure exits nonzero. `codex exec` is unchanged.

## UI

Main holds the conversation/composer; Diff, Activity, and Journal share the right
column, with Agents below. New sessions show Journal; child activity temporarily shows
Activity unless you select another pane. Narrow terminals show one pane.

| Action | Shortcut |
| --- | --- |
| Focus Main / Diff / Activity / Agents / Journal | `Ctrl-B`, then `1` / `2` / `3` / `4` / `5` |
| Resize splits | Drag dividers, or `Ctrl-B`, then arrows; Up/Down in Agents adjusts its height |
| Resize Diff navigator | `Ctrl-B`, then `[` / `]` |
| Browse Main history | `Ctrl-B`, then PageUp / PageDown |
| Return to Main | Ctrl-C |

The wheel scrolls under the pointer. Markdown supports colored code, responsive tables,
and terminal Mermaid flowcharts; unsupported diagrams remain source. `/title TITLE`
saves a manual title and skips automatic naming. Otherwise a new session may make a
separate GPT-6 Luna medium-reasoning title request; unavailable credentials or Luna
silently skip it. Notifications follow Codex's `tui.notifications`. See the [UI
contract](doc/spec/native_ui.md) for complete controls.

### Composer

- Enter steers; Tab queues. Type `?` in an empty draft for shortcuts.
- `@` attaches files; `@!` includes ignored files. VCS metadata is excluded;
  unreadable/oversized content has omission notices. Directories attach two-level
  trees honoring `.gitignore`, bounded to 256 entries/16 KiB with truncation notices.
- Enabled `$skill-name` references attach instructions, using `skills-mgr` when available.
  Use it for managed skills, `/skills` for Codex toggles; click receipts for submitted contents.
- Select Main/Activity/saved Diff text and press `r` to mention it.
- `/btw QUESTION` answers while Main works; repeat to follow up. Esc closes the panel,
  discarding the unresumable side conversation. If compaction is needed, reset Main and retry.
- `/model`, `/effort` (or `/reasoning`), and `/tier` open pickers or accept values.
  `/tier default` clears the tier. Changes reach upcoming steps without editing config.
- `/compact` resets context: Enter interrupts then resets, Tab queues. `/clear` starts
  fresh while idle, preserving sessions/files. See [reset policy](#context-reset-and-recovery).
- `/lock` prevents Esc/Ctrl-C interruption and Ctrl-C exit; `/unlock` restores them.
  The lock survives resume; explicit commands and steering still work.

Alt+Up or Shift+Left restores locally waiting input. Esc interrupts then resends only
uncommitted steers; committed input is never resent and Tab-queued input stays queued.
Without steers, Esc interrupts without resending. Ctrl-C clears the draft first, then
interrupts/restores input. Picker/help/selection dismissal takes precedence. Copying
needs OSC 52; Linux image paste needs `wl-paste` or `xclip`. See [input
details](doc/spec/native_ui.md).

### Live diff pane

Live calls stream; only completed edits become saved changes. Temporary views replace
only the editing agent's transcript. `/live` toggles them without stopping capture;
new launches enable them again. `v` switches views, `s` opens the navigator, `/` filters,
`n`/`p` changes files, `[`/`]` jumps hunks. Tab opens Changes by caller. Click an Edit
for its hunk; `Ctrl-B e` pins files, `Ctrl-B r` resumes following.
See [all diff controls](doc/spec/changes.md#live-terminal-view).

### Journal pane

Journal shows work, results, constraints, and blockers, including delegated journals.
`j`/`k` selects, Space expands, Enter opens details, `c` copies the address, Esc returns.
Unfinished work offers a countdown; Esc cancels, blockers/questions pause it.
Explicit stops stay paused across questions/forks/resume until reactivated.
Ordinary continuation keeps context; slices follow [reset policy](#context-reset-and-recovery).
See [Journal](doc/spec/journal.md).

### Agents pane

Click a child for Activity or reply links to address it. `a` filters, End resumes
following, `o` loads older history. The roster shows active time, round trips,
throughput, usage, estimated API cost, and composed captured edits. Usage/rates survive
resume while retained; `≥` marks lower bounds, `?` uncertain edits, unavailable metrics stay blank.
Main's throughput appears beside composer context. Herdr is optional; redirected
sessions keep Codex input/output. See [activity details](doc/spec/activity_display.md).

### Output dialog

Click output/errors/excerpts for retained content; `Ctrl-B !` opens the newest error.
Supported Bash/dash-backed sh lists show per-command output/status/time; missing boundaries
are labeled combined. Host-omitted text cannot be recovered. Left/Right switches tabs;
`/` searches, `n`/`N` moves matches, `y` copies, Esc/`q` closes. Select text to copy portions.
Local links read current UTF-8 files up to 8 MiB; relative paths use the workspace,
`:line` scrolls, reopening rereads. See [dialog controls](doc/spec/activity_display.md)
and [command tracking](doc/spec/execution.md).

## Recoverable output and change review

### Wrapped-session helpers

Ask the agent to use these inside a wrapped session:

| Command | Purpose | Requirements |
| --- | --- | --- |
| `mread` | Continue retained output without rerunning | Replay directory access |
| `mrun` | Bound foreground output, optionally keeping its ending | The wrapped command |
| `mchanges` | Review/compose/revert/reapply captured edits by ID | Replay directory access |
| `mcat` | Batched UTF-8 reads, ranges, tails | None |
| `msymbol` | Batched definitions/references | Go: `gopls`; JS/TS/JSON: TypeScript 7 `tsc`; Python: `pyright-langserver` |
| `inspect_file` | Structural outlines | None |

```sh
mchanges amber1..amber3 --summary
mchanges revert amber2
mcat --number source.ts 10-20 40:60
mread REF
```

Captured edits are not a live Git diff. Named ignored files are included; unknown-target
commands capture observed effects, possibly from other writers. Previews are
provisional; missing evidence is labeled. Dependency-directory contents are not retained
and cannot be reverted/reapplied. Revert merges later edits, may create conflicts, and
is captured too. See [changes](doc/spec/changes.md) and [reads](doc/spec/read.md).

## Configuration

### Mekugi settings

Create `mekugi/config.toml` under `$XDG_CONFIG_HOME` (default `~/.config` on Linux,
`~/Library/Application Support` on macOS). Optional sections are read at startup, never
rewritten:

```toml
[service_tiers]
"gpt-6-astra" = "fast"
[providers.opencode_go]
api_key = "your-go-key"
[providers.opencode_zen]
api_key = "your-zen-key"
```

Exact model IDs select tiers: `auto`, `default`, `fast` (sent as `priority`),
`priority`, or `flex`, subject to provider support. `/tier` choices override defaults
for that thread/model during the invocation. The composer shows the effective tier;
`/session` shows the provider-returned tier.

### Options

| Flag | Default | Purpose |
| --- | --- | --- |
| `--ansi-faint` | `auto` | Detect mosh; `on` uses ANSI faint, `off` fixed muted colors |
| `--mode` | `mekugi` | `passthrough` forwards Codex without Mekugi tools/plugins |
| `--vcs-guard` | `true` | Ask before UI remote writes, even with `--yolo` |
| `--post-compact-recovery` | `true` | Restore Main after provider compaction |
| `--journal-compaction` | `auto` | Select reset policy below |
| `--duplicate-output` | `false` | Reference duplicates in model input; full results/evidence stay intact |
| `--grok-auth-file` | `~/.grok/auth.json` | OAuth store |
| `--timeout` | `10m` | Wait for response start |
| `--stream-idle-timeout` | `4m` | Limit provider-message or HTTP-byte gaps |
| `--capture-output PATH` | Disabled | Append sanitized JSONL metrics |
| `--debug` | Disabled | Record private [diagnostic evidence](docs/contributing/development.md#diagnostics) |

`mekugi --mode passthrough --vcs-guard=false codex` retains Codex policy/metrics, but
disables Mekugi tools/plugins/guard and implies `--journal-compaction=off`; explicit
`auto`/`slice` is rejected. Use `--ansi-faint=off` if a multiplexer hides mosh.

### Context reset and recovery

| `--journal-compaction` | Manual/context-full requests | Slice boundaries |
| --- | --- | --- |
| `auto` (default) | Journal reset | Journal reset |
| `slice` | Provider compaction | Journal reset |
| `off` | Provider compaction | Continue in the same context |

Journal resets recover retained work/constraints/change/failure evidence without
provider summaries, including children; missing/ambiguous evidence stops reset.
Click **Context reset from journal** for retained recovery text. Interrupted resets may
need manual continuation. See [reset behavior](doc/spec/journal.md#router-answered-compaction).
The separate post-compaction hook restores Main only. Review via `/hooks` or opt out
with `--post-compact-recovery=false`; see [hook setup](doc/spec/guide.md#req-guide-002--native-post-compaction-recovery).

### Troubleshooting and integrations

- **Instructions:** wrapped sessions disable `/goal`; `<!-- mekugi:omit -->` through
  `<!-- /mekugi:omit -->` omits marked blocks without changing files/unmarked policy.
  With `skills-mgr`, managed selections attach instructions and the stock catalog is
  disabled for the session. See [guidance](doc/spec/guide.md).
- **Recovery:** see [resets](#context-reset-and-recovery) and [storage](#replay-storage).
- **Plugins:** put `.js`/`.mjs` in user-config `mekugi/plugins`; see [plugins](doc/spec/plugin.md).
- **Issue reports:** `MEKUGI_DIAGNOSE=1` enables `report_issue`, running configured
  `hooks.diagnose` commands. See [setup and effects](doc/spec/diagnose.md).
- **Environment:** router/executor must share paths/runtime directory; `MEKUGI_RUNTIME_DIR`
  overrides the temporary-directory default. Startup errors print before Codex;
  session failures show commentary/stderr. Finish sessions before replacing older
  installations; preserve unrelated settings/authentication.

### Replay storage

Records live in `$XDG_STATE_HOME/mekugi/replay`, default `~/.local/state/mekugi/replay`.
Replay never reruns commands/restores processes. Data is capped at **4 GiB**, removed
after **14 inactive days**; capacity cleanup removes oldest inactive sessions, never
running work. **Codex chats/workspace files are never deleted.** Cleanup invalidates
references; power loss can lose recent records. If retention fails, free space and retry
retention, not the operation. Stop wrappers before moving storage aside. Use `mekugi
inspect-storage`; see [storage details](doc/spec/router.md).

## Metrics

`/session` shows launch-wide usage/retries/coverage/throughput/cache/transport.
Left/Right or Tab switches tabs; Up/Down/PgUp/PgDn scrolls, `[`/`]` selects exchanges,
`r` refreshes, Esc closes. Launch totals do not restore; the roster retains thread
usage. Throughput divides authoritative output tokens (including reasoning) by measured
request seconds, including latency/retries, excluding tools/idle time. Missing
measurements are unavailable; costs are API estimates, not subscription charges. Local
estimates are not billing figures. See [metrics](doc/spec/metrics.md).

```sh
curl -sS "${MEKUGI_BASE_URL%/v1}/api/metrics"
mekugi --capture-output capture.jsonl codex
```

Captures exclude prompts, patches, and credentials.

## Documentation

[Contributing](CONTRIBUTING.md) · [Product](doc/product.md) · [Interfaces](doc/spec/index.md) ·
[Architecture](doc/architecture/index.md) · [Comparisons](doc/benchmarks.md) · [Codex
end-to-end checks](doc/codex-router-e2e.md)

## License

MIT. See [LICENSE](LICENSE).
