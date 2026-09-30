# mekugi

A mekugi is the small peg that pins a Japanese sword's handle to the blade.
Take it out and the handle comes off. Leave it in and the blade is still the
blade.

Mekugi adds compact, recoverable tools and live subagent activity to stock
Codex. Codex keeps editing, execution, the sandbox, permissions, command
sessions, and patch review. No fork, no config edits, no daemon.

[Features](#features) · [Install](#install) · [Usage](#usage) ·
[Native UI](#native-ui) · [Metrics](#metrics) · [Settings](#mekugi-settings) ·
[Troubleshooting](#configuration-and-troubleshooting) · [Documentation](#documentation)

## Features

- **Stock Codex workflow.** Code Mode JavaScript, `exec_command`, and
  `apply_patch` behave as usual. Mekugi never re-runs an edit and changes no
  configuration or instruction files. A persistent WebSocket lets a supporting
  Codex client [steer a running turn](#composer).
- **Native terminal UI.** [Main, Diff, Activity, Journal, and Agents](#native-ui) share
  one terminal without an external pane manager. The composer works like
  Codex's and adds [`/btw` side questions](#composer) that don't interrupt
  Main, plus `/model`, `/effort`, and `/tier` pickers.
- **Task journal.** A live plan strip shows current work and what remains.
  Open the Journal pane for task states, timestamps and notes. Turn cards bring
  together the outcome, new results and remaining tasks without another model request.
- **Live subagent activity.** Start notices show model and effort. Progress and
  message excerpts appear in the main conversation, or live in Mekugi’s
  [agents pane](#agents-pane), whose roster shows each agent's elapsed time,
  provider round trips, and edited lines. Encrypted messages stay private.
- **Live diffs.** Mekugi’s [live diff pane](#live-diff-pane) streams tool calls
  and provisional diffs as they arrive, then shows the saved edits. Click an
  Edit event to jump to its captured file and hunk.
- **Readable command output.** With `mekugi-exec` installed, each command in a
  list such as `cd app && make && make test` shows its own output and exit
  status and elapsed time. Click a command or read to open its retained output
  in an [output dialog](#output-dialog) with one tab per command, search, and text
  selection.
- **File attachments.** [Select files with `@`](#composer) to send text contents
  directly to the agent, without a separate file-read tool call. **Attached**
  events confirm included files; **Attach failed** events explain omissions,
  such as oversized or unreadable files.
- **Recoverable output and change review.** [Session helpers](#wrapped-session-helpers)
  continue truncated output without rerunning, and track edits from
  `apply_patch` and shell commands, with any gaps in coverage labeled. Edits can
  be reverted and reapplied by ID. Captured edits give the agent their durable
  change IDs and compact summaries in the completed tool result.
- **Fewer tokens and round trips.** Bounded and batched reads, semantic symbol
  lookup, structural outlines, scoped change IDs, and child change handoffs.
- **Leaner instructions.** Blocks marked with `<!-- mekugi:omit -->` are
  [stripped](#configuration-and-troubleshooting) before forwarding. With
  `skills-mgr`, selected skills are sent as compact references.
- **Resume, fork, and compaction continuity.** [Replay records](#replay-storage)
  keep tool history and references across resume, `/fork`, and `/side`. After
  compaction, a Codex hook restores a bounded journal and change snapshot. The
  hook is on by default; [opt out](#configuration-and-troubleshooting) with
  `--post-compact-recovery=false`.
- **Custom tools.** Add your own [plugins](doc/spec/plugin.md) as JavaScript
  modules.
- **Usage and cost.** Eligible main completions update a Markdown usage
  snapshot. A per-launch browser dashboard shows request metrics and cache
  diagnostics. Costs are API estimates, not subscription charges.
- **Diagnostics.** [Inspect past sessions](#inspect-a-session) offline, record
  debug evidence, or let agents [report issues](#configuration-and-troubleshooting)
  to your own command.
- **Other providers and tiers.** [Grok](#grok-models) and
  [OpenCode Go or Zen](#opencode-go-and-zen) run alongside OpenAI models.
  [Service tiers](#mekugi-settings) can be set per model.

These savings don't guarantee better results on every task. For controlled
comparisons, use [codex-setup-ab](https://github.com/yusing/codex-setup-ab).

## Install

Requirements:

- **Go 1.27+**, with CGO enabled and a C toolchain.
- **Codex CLI**, signed in with `codex login` using ChatGPT authentication.
- **Node.js 24+** as `node` and **ripgrep** as `rg` on the router's `PATH`.
- Any interpreter your agent picks, such as `python3`, on the executor's `PATH`.

```sh
go install github.com/yusing/mekugi/cmd/mekugi@latest github.com/yusing/mekugi/cmd/mekugi-exec@latest
codex login
mekugi codex --yolo
```

Add `$GOBIN`, or `$(go env GOPATH)/bin` if that is unset, to your `PATH`. Mekugi
prints a dashboard URL, then opens its native Main, Diff, Activity, Journal, and Agents UI.
Interactive launches currently require explicit `--yolo` (no approvals or sandbox).
Codex remains the agent runtime and tool executor. With `mekugi-exec` installed
beside `mekugi`, a command list such as `cd app && make && make test` shows each
command with its own output and exit status. Without it, the list shows as one
command.

From a checkout with **Bun** and **Make**, run `make install`. It regenerates the
embedded plugins and installs `mekugi` and `mekugi-exec`. `make uninstall`
removes only those binaries. Running sessions keep their worker executable, so
start a new session to pick up an update.

## Usage

Mekugi flags go **before** `codex`. Interactive launches accept `--yolo`, model
and config options, and [`resume`, `resume THREAD_ID` or `resume --last`](#resume); enter
prompts in the [native UI](#native-ui). Noninteractive commands keep their
ordinary Codex arguments and output:

```sh
mekugi codex --yolo --model gpt-6-sol
mekugi codex exec "Explain this repository"
mekugi codex --yolo resume 'CONVERSATION_ID'
```

Each invocation:

- starts a private router on a random loopback port and stops it when Codex
  exits. Independent sessions can run side by side. The native UI sends turn
  interrupts to Codex; noninteractive commands keep their exit status. During startup, Ctrl-C cancels without
  launching Codex.
- uses the fixed ChatGPT upstream. Standalone serving, fixed ports, custom
  provider endpoints, and `--oss` are not supported.
- forces `include_collaboration_mode_instructions=false`, and routes the
  legacy `gpt-5.6-terra` to `gpt-6-sol`.
- explicitly selects standard cybersecurity safeguards for ChatGPT requests,
  disabling automatic Daybreak selection and overriding client Daybreak choices.
- connects to ChatGPT over WebSockets, so a supporting client and model can use
  [mid-turn steering](https://developers.openai.com/api/docs/guides/steering).
  Your network must allow secure WebSockets. Mekugi falls back to HTTP only when
  ChatGPT explicitly rejects the upgrade, and never silently replays a request.
- keeps private [replay records](#replay-storage) on disk, including tool inputs
  and change evidence, so resumed and forked conversations keep their history.
- enables Codex's native session tracing in a private temporary directory and
  removes it at shutdown. The trace includes prompts and responses and has no
  disk cap. A forced kill can leave it behind.

These overrides last only for the invocation; no configuration files change.

### Headless slice plans

`mekugi codex headless --yolo` reads one prompt from stdin and runs a new
app-server thread to completion, including any planned slice continuations:

```sh
mekugi --journal-compaction=slice codex headless --yolo -m gpt-6-sol < prompt.txt
```

Use `off` to continue slices in one context, or `slice` to reset between them.
The headless client shares the native UI's reset policy with no countdown delay.
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
| `--mode` | `mekugi` | Use `passthrough` to forward traffic without Mekugi tools or plugins |
| `--post-compact-recovery` | `true` | Use `false` to skip the post-compaction context hook |
| `--journal-compaction` | `off` | Experimental `auto` uses journal summaries without a provider request; `slice` resets only between planned slices |
| `--grok` | `false` | Enable Grok models in mekugi mode |
| `--grok-auth-file` | `~/.grok/auth.json` | Select a Grok OAuth credential store |
| `--timeout` | `10m` | Wait for the upstream response to start |
| `--stream-idle-timeout` | `4m` | Limit gaps between provider messages during an active response, or HTTP response bytes |
| `--capture-output PATH` | Disabled | Append sanitized JSONL metrics |
| `--debug` | Disabled | Record diagnostics, capture, metrics, forwarded instruction/tool snapshots, runtime reads, and an AX report; print all artifact paths on exit |

`mekugi --mode passthrough codex --yolo` forwards traffic only. It doesn't need Node.js,
and capture still works.

If a detached multiplexer hides your mosh connection, use
`mekugi --ansi-faint=off codex` for readable dimmed text. The setting applies only
to that invocation and does not modify terminal configuration.

### Grok models

Authenticate with `grok login --oauth`, or set `XAI_API_KEY` in the router's
environment; an API key takes precedence. Codex credentials are never sent to
Grok.

```sh
mekugi --grok codex --yolo -m grok:grok-4.7
```

Subagents can use `grok:grok-4.5`, `grok:grok-4.6`, `grok:grok-4.7`, or
`grok:grok-4.7-build-fast` (OAuth only) in fresh context (`fork_turns="none"`).
Grok can't read encrypted OpenAI history, so switching an existing OpenAI
conversation to Grok isn't supported. See the [Grok requirements](doc/spec/grok.md).

### OpenCode Go and Zen

Set an API key here or in [Mekugi settings](#mekugi-settings):

```sh
OPENCODE_GO_API_KEY='your-key' mekugi codex --yolo -m opencode-go:glm-5.3
OPENCODE_ZEN_API_KEY='your-key' mekugi codex --yolo -m opencode-zen:kimi-k3
```

Models, reasoning controls, and prices refresh from an hourly cache. The model
picker updates on the next launch. See the [provider contract](doc/spec/opencode.md).

Grok and OpenCode requests use HTTP and their own authentication. Their models
are added to Codex's catalog for the invocation. That can't be combined with
`--profile` or `exec --ignore-user-config`; use the default configuration or an
explicit `-c model_catalog_json=...` instead.

## Native UI

An interactive `mekugi codex --yolo` launch lays out Main, Diff, Activity, Journal,
and Agents panes in one terminal without an external pane manager. Main holds the
conversation and composer. Markdown tables render as aligned grids that switch
to a record layout in narrow panes. Completed Mermaid flowchart fences render as
terminal diagrams with labeled solid/dashed edges and `&` fan-in/fan-out groups.
Unsupported syntax (including subgraphs), incomplete fences, and diagrams too
wide for the pane remain readable source.

The empty launch view shows the Mekugi and
Codex versions. Click tool output, long-content excerpts, or edit rows to open
the shared scrollable dialog without leaving your current view. Close it with
`Esc` or its top-right close button.

- `Ctrl-B`, then `1`/`2`/`3`/`4`/`5`, focuses Main, Diff, Activity, Agents, or Journal.
  Diff, Activity and Journal share the right column. Click a pane to focus it.
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

Terminal titles and desktop notifications work as in Codex and follow its
`tui.notifications` settings. Inside Herdr, its working, blocked, done, and
idle indicators update even with notifications off.

See the [native UI contract](doc/spec/native_ui.md).

### Composer

The composer works like Codex's: input history and editing keys, `@` file and
`$` skill pickers, `/skills`, Ctrl+V image paste, Ctrl+G to edit in `$EDITOR`,
Enter to steer and Tab to queue during a turn, Esc or Ctrl-C to interrupt,
Shift+Up/Down for reasoning, `/model`, and `/copy`. Type `?` in an empty
composer for the full shortcut list.

Use `/lock` to prevent Esc or Ctrl-C from interrupting work and to disable
Ctrl-C exit on an empty draft. `/unlock` restores those shortcuts. Draft clearing,
copying, and closing pickers still work while locked; `/quit` remains an explicit
exit when idle. A persistent Locked indicator shows the protection, which is
restored with the session on resume. It does not block explicit commands or
steering new input.

With Codex 0.159.0, opt in to instant steering with
`mekugi codex --yolo --enable instant_interrupt` (or
`-c features.instant_interrupt=true`). New input can then steer during model
responses and long-running Code Mode calls instead of waiting for them to finish.
Running Code Mode cells continue in the background; steering does not terminate
them. This is off by default; `--disable instant_interrupt` opts out. The same
options work with `resume`.

Mekugi differs in these ways:

- **File contents attach.** `@!` also finds ignored files; neither picker lists
  VCS metadata such as `.git`. Selected text files attach their contents when
  you submit or queue the prompt, and large or unreadable files produce an
  explicit omission notice instead of truncated content.
- **Select to mention.** Drag across Main, Activity, or the saved Diff and press
  `r` to add the selection as one short mention, such as `[Selected message]` or
  `[Selected diff hunk @amber1:42-45]`. The selected text is sent with the
  prompt without filling the composer.
- **Skill references attach automatically.** Complete enabled `$skill-name`
  references bind when you finish the word or submit a pasted prompt. Composer
  and transcript use the same amber highlighting; unknown, disabled, or
  ambiguous names stay plain text.
- **Answer images attach.** Paste an image or its file path into a question
  answer to get the same `[Image N]` attachment as in the main composer.
  Synchronous answers send their images in a companion message after the
  question is resolved; asynchronous answers send them with the reply.
- **Waiting messages combine.** Steers typed while an earlier one is still
  sending, and queued messages, are sent together as one message, one entry per
  line. Alt+Up or Shift+Left brings back the last queued message.
- **Session controls.** `/compact` compacts context, waiting for the current turn
  when busy. `/clear` starts a fresh session and clears its transcript; it is
  available while idle and does not delete saved sessions or filesystem changes.
  Interrupt returns unsent input to the composer without automatically resending
  it. Interrupting an uncommitted first message leaves an empty transcript.
- **Live pane.** `/live` opens an on/off picker; `/live on` or `/live off`
  applies directly for the current session. Hiding it frees its space without
  stopping activity or edit capture. New launches, including resume, show it again.
- **Setting pickers.** `/model`, `/effort` (also `/reasoning`), and `/tier` open
  a picker: Up/Down and Enter apply a choice, Esc cancels, and neither the
  command nor its choices enter the transcript. A value switches directly, such
  as `/effort high` or `/tier priority`; `/tier default` clears the tier.
  Changes also reach a running turn's next steps and never edit your config file.
- **Terminal requirements.** `/copy` and text selection copy through OSC 52,
  which your terminal must allow. Pasting a clipboard image on Linux needs
  `wl-paste` (Wayland) or `xclip` (X11).

`/btw QUESTION` asks a side question about a snapshot of the conversation, even
while Main is working. The answer streams in a panel above the composer. Repeat
`/btw QUESTION` to follow up, use PgUp/PgDn to scroll, and press Esc to close;
Main keeps working and your draft stays. Closing discards the side
conversation, and it can't be resumed.

### Resume

`resume THREAD_ID` and `resume --last` work as in Codex. Resuming also restores
pane layout, keyboard focus, the agent roster, Activity history, and the
session's retained Diff changes. Scroll positions, filters, selections, and
drafts are not restored. Threads with more than 16 MiB of history can't resume
in the native UI.

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
  and scroll positions unchanged. `Esc` dismisses the dialog.
- Browsing pauses following; `r` resumes.

See [live view details](doc/spec/changes.md#live-terminal-view).

### Journal pane

Press `Ctrl-B` then `5` to open Journal. Its title counts tasks by state. Open tasks
come first; finished subtrees start collapsed. Use `j`/`k` or arrows to select,
`Space` to expand or collapse, `d` to read full details, `Enter` to open a row, and
`c` to copy the selected path. Click a `▸` marker to expand it, or click a row to open it.
When requests without workspace metadata keep a separate unscoped journal, the title
offers `n` to switch between it and the workspace journal. Notes
from the other journal still appear in Main.
`Esc` returns to Main. Blocked tasks show their reason; notes have no state label.
Delegated journals appear under their owning task, or in an Agents group. Press
`Enter` on, or click, a mounted agent to open its Activity. Lifecycle labels reflect observed
host state; a provider answer alone does not mark an agent complete.

The plan strip stays above the composer while work remains. Main shows task
transitions, plus notes while Journal is hidden. A turn card contains its outcome,
new work and remaining tasks; select its expansion target to see the notes.

### Agents pane

The **Activity** pane streams child activity, messages, and replies, including
while Main waits. The **Agents** roster below the main columns shows children
with their elapsed time, provider round trips, and edited lines; Main's
conversation and progress stay in Main. Token and cost figures come from the
router's usage accounting, not an additional app-server total.

- Click an agent to inspect its activity; reply links address that agent.
- Scrolling pauses following; `r` resumes it. The mouse wheel scrolls without
  changing keyboard focus.

Redirected sessions keep ordinary Codex input/output and inline agent activity.
Inside Herdr, Mekugi advertises the invocation through Herdr's agent hint.
Herdr is optional and does not control Mekugi's internal panes.

### Output dialog

Click a command, program, read, or long-content excerpt in Main or Activity to
open its full content in the shared dialog above the panes, without expanding
the transcript. Read source and unified diffs use syntax colors. When
`mekugi-exec` recorded a command list, each command gets its own tab with its
output, exit status, start/end timestamps and measured duration. Decorative
section headings are omitted. Older history without timing evidence shows no
per-command duration; an invocation's total is never reused for its commands.
Without retained output boundaries, the dialog labels the output as combined.

- Left/Right or a click switches tabs. `j`/`k`, `PgUp`/`PgDn`, and `g`/`G`
  scroll; live output follows its tail until you scroll up.
- `/` searches, and `n`/`N` step through matches.
- `y` copies the page's output. Drag to select text, then `y`, `c`, or Ctrl-C
  copies the selection.
- The top-right `[×]` button, `Esc`, `q`, or a click outside closes it.

See [activity display](doc/spec/activity_display.md).

## Editing and execution

Codex owns editing and execution. Mekugi passes stock `apply_patch` and
`exec_command` arguments and results through unchanged. In Code Mode,
`Promise.allSettled` runs independent calls in parallel and retains each outcome:

```js
const results = await Promise.allSettled([
  tools.exec_command({cmd: "rg -n 'TODO' src"}),
  tools.exec_command({cmd: "mcat README.md 1:80"}),
]);
for (const result of results) {
  if (result.status === "fulfilled") text(result.value.output);
  else text(String(result.reason));
}
```

Long-running commands continue through Codex's `write_stdin` session IDs.

### Wrapped-session helpers

These executables are on Codex's `PATH` only inside a wrapped session. The agent
learns them from its tool guidance, and you can ask it to use them.

| Command | Purpose | Extra prerequisite on the executor's `PATH` |
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

`mchanges` counts come from recorded changes, not a live Git diff. Like
`git revert`, `mchanges revert` merges any later edits and marks overlaps with
conflict markers. The revert is recorded as a new change, so it can be undone
too. See the [change record](doc/spec/changes.md), [reader](doc/spec/read.md),
and [execution contract](doc/spec/execution.md).

## Metrics

The dashboard URL printed at startup works only while that session runs. Over
SSH, forward its port first.

```sh
curl -sS "${MEKUGI_BASE_URL%/v1}/api/metrics"
mekugi --capture-output capture.jsonl codex
```

The router writes a Markdown token-usage snapshot to the system temporary
directory after each eligible main completion, reusing the same file for the
same Codex session, and prints its path when Codex exits. Dashboard metrics
stay in memory unless captured with `--capture-output`; captures hold sanitized
measurements only, with no prompts, patches, or credentials. Provider-reported
usage is authoritative; local token estimates are not billing figures. See the
[metrics reference](doc/spec/metrics.md).

`mekugi --debug codex` writes a private `mekugi-debug-*` directory in the system
temporary directory and prints its paths on exit. Its instruction dumps are
**not sanitized**. Debug mode records future requests only. See
[debug evidence](doc/spec/router.md#feature-usage-debug-evidence).

## Mekugi settings

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
  choose.
- **API keys:** `OPENCODE_API_KEY` overrides both file keys. The per-service
  variables override their own service. Setting one to an empty string turns
  that service off.

## Configuration and troubleshooting

- **Post-compaction recovery:** Mekugi registers a Codex `SessionStart` hook that
  matches `compact` and pre-trusts only that hook for the session. It never
  bypasses trust for other hooks. After each compaction, the hook restores a
  bounded snapshot of the main thread's journal and changes. Subagents and
  passthrough mode are unaffected. It needs Codex's compact `SessionStart`
  support (tested with CLI 0.156.1). If a Codex update changes how hooks are
  hashed, Codex asks you to review the hook under `/hooks` instead. Opt out with
  `--post-compact-recovery=false`, or disable the hook in `/hooks`. If you pass
  `hooks` through `-c`, Mekugi leaves them alone and prints a notice. In that
  case, add a `SessionStart` handler matching `^compact$` that runs
  `/absolute/path/to/mekugi post-compact`. Hook failures don't stop the task.
  See [guidance behavior](doc/spec/guide.md).
- **Journal compaction (opt-in):** `--journal-compaction=auto` uses retained task,
  constraint, change and failed-command evidence instead of asking the model for a
  summary. It includes subagents and falls back to the provider if evidence cannot
  be safely recovered. A proven router summary skips duplicate hook injection.
  The default remains `off` pending comparative evaluation; this is not evidence
  of improved model success or token savings. In the native UI, sliced plans show
  a countdown between successful turns; Esc cancels. `slice` and `auto` reset
  context before continuing, while `off` continues without resetting. An interrupted
  reset may require manual continuation. See [compaction behavior](doc/spec/journal.md#router-answered-compaction).
- **Instructions:** Wrapped sessions disable Codex's `/goal` feature. Mekugi keeps
  Codex's base instructions and adds its guidance through tool descriptions. Anything between `<!-- mekugi:omit -->` and
  `<!-- /mekugi:omit -->` in instructions, including `AGENTS.md`, is removed before
  forwarding. When `skills-mgr` is on the `PATH`, Mekugi turns off Codex's stock
  skill catalog for the session and sends each selected skill as
  `<skill name="…"/>`. See [guidance behavior](doc/spec/guide.md).
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
processes.

Mekugi deletes session data after **14 days without activity**. When storage is
full, it removes the least recently active inactive sessions first, and never
touches running work. **Your Codex chats and workspace files are never deleted.**
Cleanup can break old recovery and review references. To reset, stop all Mekugi
wrappers and move the directory aside.

### Inspect a session

This reads a Codex rollout without running anything or starting a router:

```sh
mekugi inspect-session --session /path/to/rollout.jsonl
mekugi inspect-session --failures
mekugi inspect-sessions --exclude-model '*grok*' --class production
```

The default JSON holds tool names, call IDs, outcomes, and sizes, but no
private text. Values you request with `--field` may include source and command
output. See [session inspection](doc/spec/session.md) and
[AX evidence](doc/spec/ax.md).

### Older installations

Finish active sessions before replacing an older installation. Retire any old
service and provider configuration separately, keeping unrelated settings and
authentication. Use `mekugi codex` from then on.

## Go library

The root package provides the review-diff rendering helpers behind the router's
change evidence.

## Documentation

- [Product specification](doc/product.md)
- [Interface contracts](doc/spec/index.md)
- [Architecture ownership](doc/architecture/index.md)
- [Controlled comparisons](doc/benchmarks.md)
- [Codex end-to-end checks](doc/codex-router-e2e.md)

## Development

To review the native app-server UI without Codex or model requests, run
`make preview-native-ui` in a terminal. It plays a synthetic session through the
real panes and renderer: streaming and long messages, journal edits, retraction
and flush, agent summaries, and Main/agent communication. Scroll, resize, and
click reply links to inspect them. Keys behave as in the real UI: Enter steers
the playing turn or, once idle, starts a new one that echoes your prompt; Tab
queues for the next turn; Ctrl-C clears the draft, then interrupts playback,
then exits, as does `/quit`.

Bun is required to regenerate and test plugin assets:

```sh
go generate ./internal/router/toolplugin
bun test ./internal/router/toolplugin/tests
go test ./...
go vet ./...
```

## License

MIT. See [LICENSE](LICENSE).
