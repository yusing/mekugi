# mekugi

A mekugi is the small peg that pins a Japanese sword's handle to the blade.
Take it out and the handle comes off. Leave it in and the blade is still the
blade.

Mekugi adds compact, recoverable tools and live subagent activity to stock
Codex. Codex keeps editing, execution, the sandbox, permissions, command
sessions, and patch review. No fork, no config edits, no daemon. The
[Claude backend preview](#claude-code-preview) uses the official runtime in the same UI.

[Features](#features) · [Install](#install) · [Usage](#usage) ·
[Native UI](#native-ui) · [Metrics](#metrics) · [Configuration](#configuration) ·
[Documentation](#documentation)

## Features

- **Claude Code preview.** `mekugi claude` offers streaming conversation, native
  permission decisions, shared live/saved Diff, live command output with supported
  Bash command segments, retaining `!command` input, saved-session title/resume/clear
  controls, Bash utilities and durable journals in the existing interface, without an inference router. See its
  [current limits and build instructions](#claude-code-preview).

### Agent-facing

- **Fewer tokens and round trips.** [Session helpers](#wrapped-session-helpers)
  offer bounded and batched reads, semantic symbol lookup, and structural outlines.
- **Recoverable output and changes.** Continue omitted output without rerunning,
  and review, revert, or reapply captured edits by ID. Gaps in coverage are labeled.
- **Leaner instructions.** [Omit marked instruction blocks](#troubleshooting-and-integrations)
  for the session without changing instruction files or unmarked policy.
- **Task journal.** Record plans, results, constraints, and blockers as durable
  work state, rather than repeating status summaries in conversation.
- **Session continuity.** Retained journal and change evidence survives resume,
  forks, side threads, and compaction, subject to [storage retention](#replay-storage).
- **Custom tools.** Extend the agent's capabilities with your own
  [JavaScript tool plugins](doc/spec/plugin.md).

### User-facing

- **Auto resume accidental stop.** Continue unfinished journal work when the agent
  stops, including after answering a follow-up. Explicit user stops, blockers, and
  pending questions are respected. See the [Journal pane](#journal-pane).
- **Native terminal UI.** [Main, Diff, Activity, Journal, and Agents](#native-ui)
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
  With `mekugi-exec`, command lists show each command's output, exit status, and timing.
- **File, directory and skill attachments.** [Send selected contents or a bounded directory tree directly](#composer)
  without a separate agent read; unreadable or oversized attachments have explicit notices.
- **Composer additions.** Mention selected messages or diffs, and ask
  [`/btw` side questions](#composer) without interrupting Main.
- **Usage, throughput and cost.** See per-thread usage, output tokens/sec and
  estimated API costs in Agents, plus launch-wide average throughput, usage
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
streams appear as provisional proposals in the shared live-edit dock and Diff
pane. Partial content does not claim deletion of an unseen suffix. Native tool
completion does not turn these proposals into saved edit evidence.

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
receive them with the native preset; resumed sessions receive current guidance on
their first input without replacing the saved prompt. Children and compaction
receive the same guidance. The catalog file remains a recovery reference, not a
required extra read. Completed foreground captures
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

Bash edit previews, native terminal foreground switching and full rich-preview
continuity remain unfinished. Command
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
| `mekugi --yolo` | Authenticated third-party providers, using `grok:` and `opencode*:` IDs | [Grok credentials](#grok-models) or [OpenCode API keys](#opencode-go-and-zen); no Codex login |
| `mekugi grok --yolo` | Grok only, using plain IDs such as `grok-4.7` | [Grok credentials](#grok-models); no Codex login |
| `mekugi codex --yolo` | Codex only, no third-party models | `codex login` with ChatGPT authentication |

Standalone mode fails at startup if no third-party provider has credentials.
Its default uses [Grok's model selection](#grok-models) when authenticated,
otherwise the first available OpenCode Go model, then Zen.

Mekugi opens its native terminal workspace. Codex-backed interactive launches
currently require explicit `--yolo` (no approvals or sandbox).

Mekugi flags go **before** `codex`, `grok`, or standalone Codex arguments.
Interactive launches accept `--yolo`, model
and config options, and [`resume`, `resume THREAD_ID` or `resume --last`](#resume); enter
prompts in the [native UI](#native-ui). Noninteractive commands keep their
ordinary Codex arguments and output:

```sh
mekugi exec "Explain this repository"
```

### Launch behavior and limits

Independent sessions can run side by side. Ctrl-C during startup cancels the
launch. On native UI exit, Mekugi prints commands to resume with the original
options or replay offline. Noninteractive commands retain Codex's exit status.

- There is no standalone router or daemon. Fixed ports, custom provider endpoints,
  and `--oss` are not supported.
- OpenAI sessions need secure WebSockets to ChatGPT. Mekugi falls back to
  HTTP only when ChatGPT explicitly rejects the upgrade, and never silently
  replays a request.
- Grok and OpenCode requests use HTTP and their own credentials. Third-party
  credentials do not enable those providers in `mekugi codex`.
- Invocation-only overrides disable collaboration-mode instructions, route
  `gpt-5.6-terra` to `gpt-6-sol`, and select standard cybersecurity safeguards
  rather than Daybreak. No configuration files change.
- Private [replay records](#replay-storage) include tool inputs and change
  evidence. Native Codex tracing also records prompts and responses in a
  temporary directory with no disk cap; it is removed at shutdown, but a
  forced kill can leave it behind.

### Headless slice plans

The `headless --yolo` subcommand reads one prompt from stdin and runs a new
session to completion, continuing unfinished journal work and any planned slices:

```sh
mekugi --journal-compaction=slice headless --yolo < prompt.txt
```

Use `off` to continue slices in one context, or `slice` to reset between them.
Unlike the native UI, headless journal continuations have no countdown delay.
It accepts model and `-c` options, not resume or positional prompts. Prompts must
be nonempty and at most 16 MiB. Like the native UI, it requires explicit `--yolo`.
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
| `--post-compact-recovery` | `true` | Use `false` to skip the post-compaction context hook |
| `--journal-compaction` | `off` | Experimental `auto` uses journal summaries without a provider request; `slice` resets only between planned slices |
| `--grok-auth-file` | `~/.grok/auth.json` | Select a Grok OAuth credential store |
| `--timeout` | `10m` | Wait for the upstream response to start |
| `--stream-idle-timeout` | `4m` | Limit gaps between provider messages during an active response, or HTTP response bytes |
| `--capture-output PATH` | Disabled | Append sanitized JSONL metrics |
| `--debug` | Disabled | Record diagnostics, capture, metrics, forwarded instruction/tool snapshots, runtime reads, and an AX report; print a session diagnosis command on exit |

`mekugi --mode passthrough codex --yolo` forwards traffic only. It doesn't need Node.js,
and capture still works.

If a detached multiplexer hides your mosh connection, use
`mekugi --ansi-faint=off codex --yolo` for readable dimmed text. The setting applies only
to that invocation and does not modify terminal configuration.

### Grok models

Authenticate with `grok login --oauth`, or set `XAI_API_KEY` in the router's
environment; an API key takes precedence. Codex credentials are never sent to Grok.

```sh
mekugi grok --yolo -m grok-4.7
mekugi --yolo -m grok:grok-4.7
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
OPENCODE_GO_API_KEY='your-key' mekugi --yolo -m opencode-go:glm-5.3
OPENCODE_ZEN_API_KEY='your-key' mekugi --yolo -m opencode-zen:kimi-k3
```

Models, reasoning controls, and prices refresh from an hourly cache. The model
picker updates on the next launch. See the [provider contract](doc/spec/opencode.md).

`OPENCODE_API_KEY` overrides both service keys from the settings file. Per-service
variables override it and any file keys; an empty per-service value disables that service.

## Native UI

This section describes the Codex-backed interface. Claude uses the same pane
shell with the capabilities and limits in [Claude Code preview](#claude-code-preview).

Every interactive launch lays out Main, Diff, Activity, Journal,
and Agents panes in one terminal without an external pane manager. Main holds the
conversation and composer. Inline Markdown code uses shell syntax colors; fenced
code uses its language tag. Markdown tables render as aligned grids that switch
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

Terminal titles and desktop notifications work as in Codex and follow its
`tui.notifications` settings. Inside Herdr, its working, blocked, done, and
idle indicators update even with notifications off.

See the [native UI contract](doc/spec/native_ui.md).

### Composer

Use Codex's familiar input editing, file and skill pickers, steering, and
queuing: Enter steers a running turn; Tab queues input. Type `?` in an empty
composer for the full shortcut list. The
Mekugi-specific controls and attachment behavior are described below.

Use `/lock` to prevent Esc or Ctrl-C from interrupting work and to disable
Ctrl-C exit on an empty draft. `/unlock` restores those shortcuts. Draft clearing,
copying, and closing pickers still work while locked; `/quit` remains an explicit
exit when idle. A persistent Locked indicator shows the protection, which is
restored with the session on resume. It does not block explicit commands or
steering new input.

Mekugi differs in these ways:

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
  the turn ends; interrupt to restore input that has not committed.
- **Session controls.** Enter on `/compact` interrupts the current turn and compacts
  after Codex acknowledges its end, with or without `instant_interrupt`. Tab queues
  compaction until the current turn ends. After successful busy compaction, waiting
  input runs next; without waiting input, Mekugi sends a visible continuation
  message to resume the task.
  Idle compaction stays idle, and failure or interruption never automatically
  continues it. Ctrl+C cancels queued compaction and restores waiting input
  without interrupting Main. Cancelling while an Enter-command interruption is
  pending prevents compaction but cannot undo that interruption; once compaction
  starts, it interrupts only that compaction. `/clear` starts a fresh session and clears its transcript; it is
  available while idle and does not delete saved sessions or filesystem changes.
  Interrupt returns unsent input to the composer without automatically resending
  it. Interrupting an uncommitted first message leaves an empty transcript.
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
Compact Main, close the side panel, and retry to take a fresh snapshot.

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
histories larger than 16 MiB cannot resume in the native UI. Older child activity loads on demand.
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
- `s` shows or hides the file tree, `t` toggles tree/flat paths, `/` filters
  files.
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
- Browsing pauses following; `r` resumes.

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
with their active elapsed time, provider round trips, current-round output tokens/sec,
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
last-response age continues counting. Output throughput also appears beside context
in Main's composer.
A round is one forwarded provider request, not a user turn. Its measured rate
appears when provider usage arrives and clears when the next request starts;
no streamed token estimates are shown. Retained last-round measurements survive
resume, but missing timing stays blank. See [Metrics](#metrics) for launch-wide totals.

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
their 256 KiB limit. When `mekugi-exec` recorded a command list, each command gets
its own tab with its
output, exit status, start/end timestamps and measured duration. Older history
without timing evidence shows no per-command duration.
Without retained output boundaries, the dialog labels the output as combined.
Errors keep a short inline preview. Click **details**, or focus Main or Activity
and press `Ctrl-B` then `!`, to read the full error, including restored Code Mode
failures. The keyboard shortcut opens the newest error; Left/Right reaches the
other retained errors in that transcript. Host-omitted text cannot be recovered.

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
preparation, tool execution, and idle time between requests are excluded. Requests
without timing or authoritative token totals are excluded from both sums; measured
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
mekugi --capture-output capture.jsonl codex --yolo
```

Launch metrics stay in memory unless captured with `--capture-output`;
captures hold sanitized measurements only, with no prompts, patches, or
credentials. Provider-reported usage is authoritative; local token estimates
are not billing figures. Costs are API estimates, not subscription charges. See
the [metrics reference](doc/spec/metrics.md).

`mekugi --debug codex --yolo` writes a private `mekugi-debug-*` bundle under
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
  choose. The native composer and `/status` show the effective tier, including
  this override, rather than only Codex's requested tier.
  `/tier` still changes Codex's requested tier and discloses any overriding
  Mekugi setting. `/session` reports the provider-returned tier, which can differ
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
- **Journal compaction (opt-in):** `--journal-compaction=auto` uses retained task,
  constraint, change and failed-command evidence instead of asking the model for a
  summary. It includes subagents and falls back to the provider if evidence cannot
  be safely recovered.
  Recovery keeps constraints and open work inline; completed task bodies and agent
  history stay available through journal reads when needed, rather than filling
  the restored context.
  The default remains `off` pending comparative evaluation; this is not evidence
  of improved model success or token savings. In the native UI, sliced plans show
  a countdown between successful turns; Esc cancels. `slice` and `auto` reset
  context before continuing, while `off` continues without resetting. An interrupted
  reset may require manual continuation. Resets appear once in the journal, not as
  duplicate commentary. Click the journal's **Context reset from journal** row to
  read the recovery message shown to the model. Older or expired messages are
  marked unavailable. See [compaction behavior](doc/spec/journal.md#router-answered-compaction).
- **Instructions:** Wrapped sessions disable Codex's `/goal` feature. Anything
  between `<!-- mekugi:omit -->` and `<!-- /mekugi:omit -->` in instruction files
  is omitted for the session; the files themselves are not changed. When
  `skills-mgr` is on the `PATH`, Mekugi turns off Codex's stock skill catalog
  for the session. Managed `$skill` selections attach their
  instructions in the native composer; other Codex-selected skill injections
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

Review a retained session through the current native UI without running Codex,
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

For profiling, use `--headless --width 160 --height 48` with `--cpu-profile` or
`--heap-profile` pointing to a new file. Headless playback renders the UI but
cannot measure terminal backpressure. Compare runs with the same inputs, seed,
speed, and dimensions; use 1.0x for representative latency. Session contents
remain local and may appear on screen. See the [replay reference](doc/spec/session_replay.md).

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

The [build workflow](.github/workflows/release.yml) checks the bridge offline and
packages both commands with the private Claude bridge. It smoke-tests extracted,
relocated Claude startup without model requests for Linux amd64/arm64 and macOS
arm64 on pushes to `main`, pull requests, and manual runs. Pushing a `v*` tag also publishes the three archives and `SHA256SUMS` to a
GitHub release, with the tag embedded as the welcome version. Rerunning a tag build
replaces that release's matching assets.

To review the native app-server UI without Codex or model requests, run
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
and live-model acceptance remain outstanding.
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

Built-in frontends are native Go. Go regenerates the optional plugin shared core;
Bun is needed only to run the JavaScript plugin-host and shared-core tests:

```sh
go generate ./internal/router/toolplugin
bun test ./internal/router/toolplugin/tests
make test
go vet ./...
```

While implementing, select the affected Go packages and tests, for example
`make test TEST_PACKAGES=./internal/router TEST_RUN='^TestShellRunnerMRun'`.
The default checks all packages and uses Go's test cache; add
`TEST_FLAGS=-count=1` for an uncached run. See [focused checks](CONTEXT-TESTS.md#focused-checks).

## License

MIT. See [LICENSE](LICENSE).
