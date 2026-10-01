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
  configuration or instruction files. The native composer lets you
  [steer a running turn](#composer).
- **Native terminal UI.** [Main, Diff, Activity, Journal, and Agents](#native-ui) share
  one terminal without an external pane manager. The composer works like
  Codex's and adds [`/btw` side questions](#composer) that don't interrupt
  Main, plus `/model`, `/effort`, and `/tier` pickers.
- **Task journal.** A live plan strip shows current work and what remains.
  Open the Journal pane for task states, timestamps and notes. Separate work reports
  show new results and remaining tasks without another model request; ordinary replies
  stay in the conversation.
- **Live subagent activity.** Start notices show model and effort. Progress and
  message excerpts appear in the main conversation, or live in Mekugi’s
  [agents pane](#agents-pane), whose roster shows each agent's elapsed time,
  provider round trips, and cumulative edit activity, with the composed net
  outcome in its header. Encrypted messages stay private.
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
  [stripped](#configuration-and-troubleshooting) before forwarding, without
  changing caller-owned policy outside those blocks.
- **Resume, fork, and compaction continuity.** [Replay records](#replay-storage)
  keep tool history and references across resume, `/fork`, and `/side`. After
  compaction, a Codex hook restores a bounded journal and change snapshot. The
  hook is on by default; [opt out](#configuration-and-troubleshooting) with
  `--post-compact-recovery=false`.
- **Custom tools.** Add your own [plugins](doc/spec/plugin.md) as JavaScript
  modules.
- **Usage and cost.** The native Agents roster shows per-thread usage and cost.
  The `/session` dialog shows launch-wide request metrics, transport and cache
  diagnostics. Costs are API estimates, not subscription charges.
- **Diagnostics.** [Replay session UI](#replay-a-session) with adjustable speed and
  CPU/heap profiles, [inspect past sessions](#inspect-a-session) offline, record
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
opens its native Main, Diff, Activity, Journal, and Agents UI.
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
mekugi codex --yolo --model gpt-6.1-sol
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
- skips Codex model prewarm by sending Codex-to-router turns over HTTP. The
  router still connects to ChatGPT over WebSockets. Codex owns native steering
  and instant interruption; direct Responses WebSocket controls are not used
  by the wrapped HTTP client.
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
  explicit omission notice instead of truncated content. Bound file references
  use the same green highlighting in the composer and submitted messages.
- **Select to mention.** Drag across Main, Activity, or the saved Diff and press
  `r` to add the selection as one short mention, such as `[Selected message]` or
  `[Selected diff hunk @amber1:42-45]`. The selected text is sent with the
  prompt without filling the composer. Selection copying and mentions preserve
  Markdown formatting, selected table cells, code whitespace, and hard breaks,
  without panel decoration.
- **Skill references attach automatically.** Complete enabled `$skill-name`
  references bind when you finish the word or submit a pasted prompt. Composer
  and transcript use the same amber highlighting; unknown, disabled, or
  ambiguous names stay plain text. With `skills-mgr`, managed skills attach
  their actual instructions automatically, with an `Attached skill` receipt;
  unreadable or oversized contents produce an explicit omission notice.
  Without it, contents come from the skill file identified by Codex's metadata. Manage
  managed skills with `skills-mgr`; `/skills` toggles Codex-discovered skills.
- **Answer images attach.** Paste an image or its file path into a question
  answer to get the same `[Image N]` attachment as in the main composer.
  Synchronous answers send their images in a companion message after the
  question is resolved; asynchronous answers send them with the reply.
- **Waiting messages combine.** Steers typed while an earlier one is still
  sending, and queued messages, are sent together as one message, one entry per
  line. Alt+Up or Shift+Left brings back the last queued message.
- **Session controls.** `/compact` compacts context, waiting for the current turn
  when busy. After successful queued compaction, waiting input runs next; without
  waiting input, Mekugi sends a visible continuation message to resume the task.
  Idle compaction stays idle, and failure or interruption never automatically
  continues it. Ctrl+C cancels queued compaction and restores waiting input
  without interrupting Main; once compaction starts, it interrupts only that
  compaction. `/clear` starts a fresh session and clears its transcript; it is
  available while idle and does not delete saved sessions or filesystem changes.
  Interrupt returns unsent input to the composer without automatically resending
  it. Interrupting an uncommitted first message leaves an empty transcript.
- **Reasoning stays visible.** Main and Activity display short summaries directly,
  adding `for <duration>` without a preceding period when timing is available.
  Longer summaries roll through like command output and remain readable until
  later activity settles. Titled sections collapse to their own titles; untitled
  reasoning uses `Thought`. Click a section to read its full text in a dialog.
- **Live pane.** `/live` toggles it on or off; `/live on` or `/live off`
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
while Main is working. The answer streams in a panel above the composer, with
shimmering Answering status and elapsed time. Drag across its content to copy or
reference it, just as in Main. Repeat
`/btw QUESTION` to follow up, use PgUp/PgDn to scroll, and press Esc to clear a
selection or close the panel;
Main keeps working and your draft stays. Closing discards the side
conversation, and it can't be resumed.

### Resume

`resume THREAD_ID` and `resume --last` work as in Codex. Startup resume and `/resume`
restore the session's saved model, reasoning effort, and service tier; explicit
`-m` and `-c` flags override only the settings they name. Resuming also restores
pane layout, keyboard focus, the agent roster, Activity history, and the
session's retained Diff changes. Scroll positions, filters, selections, and
drafts are not restored. Successfully applied settings survive a fresh launch,
including priority and default selections made before the first turn. Storage
failures are reported in Activity. Older sessions can recover settings from the
last 8 MiB of Codex history, with newer history taking precedence over retained preferences.
If no saved model settings are available, a notice explains that Codex defaults
and explicit flags apply instead. Main histories larger than 16 MiB cannot resume
in the native UI. Paginated child histories load recent Activity first; older
content remains available on demand rather than delaying the conversation.
Codex does not save an empty conversation before its first turn; retained
settings do not make an unsaved conversation resumable.
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
When requests without workspace metadata keep a separate unscoped journal, the title
offers `n` to switch between it and the workspace journal. Notes
from the other journal still appear in Main.
`Esc` returns to Main. Blocked tasks show their reason; notes have no state label.
Delegated journals appear under their owning task, or in an Agents group. Press
`c` to copy their full journal address; rows and details show readable agent names
and local paths instead of internal mount IDs. Press
`Enter` on, or click, a mounted agent to open its Activity. Lifecycle labels reflect observed
host state; a provider answer alone does not mark an agent complete.

The plan strip stays above the composer while work remains, preferring working
tasks over blocked or pending tasks, then the newest update within that state.
Main shows task
transitions, plus notes while Journal is hidden. Answers stay in the conversation.
When there are new work updates, a separate journal card shows the new work and
unchanged remaining tasks without repeating changed tasks. It previews the newest
three notes and short blocker reasons; open the card for complete evidence and
older notes. Completed reports taller than eight body rows collapse to a compact
disclosure; small cards stay readable, and clicking still opens the full report.
An unchanged plan does not add a card to an ordinary reply.

With Codex `exec --json`, child journal milestones appear in Main's next response,
separately from the child's final result. A host tool wait can delay those updates;
the native Journal and Activity panes remain live while Main waits.

### Agents pane

The **Activity** pane streams child activity, messages, and replies, including
while Main waits. The **Agents** roster below the main columns shows children
with their elapsed time, provider round trips, and cumulative edited lines as
`+N -N`. The roster header's `+N -N` reports the final composed outcome
across agents, so superseded edits and files created then deleted do not inflate it.
It uses recorded changes, not a live Git diff; incomplete or inconsistent evidence
shows `?`. Main's conversation and progress stay in Main. Token and cost figures
come from the router's usage accounting, not an additional app-server total.

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

Click a command, program, read, or long-content excerpt in Main or Activity to
open its full content in the shared dialog above the panes, without expanding
the transcript. Read source and unified diffs use syntax colors. When
`mekugi-exec` recorded a command list, each command gets its own tab with its
output, exit status, start/end timestamps and measured duration. Decorative
section headings are omitted. Older history without timing evidence shows no
per-command duration; an invocation's total is never reused for its commands.
Without retained output boundaries, the dialog labels the output as combined.

- Tab paths shorten from the front, retaining the nearest directories and filename.
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

Open `/session` in Main for the current launch's request counts, provider usage,
cache diagnostics, capture health, transport, tool measurements, and retained
exchange details. Left/Right or Tab switches Overview, Transport, and Exchanges;
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
are not billing figures. See the
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
  choose. The native composer and `/status` show the effective tier, including
  this override, rather than only Codex's requested tier.
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
  skill catalog for the session. Managed `$skill` selections attach their
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
processes.

Snapshots share repeated contents and use compressed storage. For read-only
diagnosis, inspect one retained call without executing it:

```sh
mekugi inspect-storage --workspace /absolute/workspace --call-id CALL_ID
mekugi inspect-storage --workspace /absolute/workspace --call-id CALL_ID --field baseline
```

The default returns sizes, hashes and dependency counts, not private text.
`--field` selects `all`, `baseline`, `exec`, `patches`, `review`, or `refs`.
Text is limited to 4 KiB; use `--text-bytes` (up to 64 KiB) and the returned
`next_offset` with `--offset` to continue. `--field refs` identifies compressed
objects, which can be inspected with `--object NAME --field all`.
`--replay-dir DIR` selects a different store. Missing or corrupt evidence produces
a precise error, not an empty successful snapshot.

Mekugi retains up to **4 GiB** of managed session data and deletes it after
**14 days without activity**. When storage is
full, it removes the least recently active inactive sessions first, and never
touches running work. **Your Codex chats and workspace files are never deleted.**
Cleanup runs in the background without holding up a complete session-start sweep.
At the storage limit, response translation waits up to 30 seconds for background
cleanup and retries saving the same evidence, not the request or host operation.
If space remains unavailable, retention reports that cleanup was requested; retry
retaining the evidence after space is freed, not the completed host operation.
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

Playback defaults to **1.0x**. It preserves recorded item start/end times and gaps,
including command duration and retained question/answer arrivals. Referenced child
rollouts in the same directory are included. Optional debug captures add provider
request intervals. Streaming is **simulated**, not a screen recording: retained
text is released in seeded, irregular chunks within its recorded interval. The
default `--seed 1` makes comparisons repeatable. Missing children and unsupported
item kinds are reported. Retained journal messages remain visible; internal
journal transport commands are hidden only when local retained provenance
identifies them. Unverified candidates stay visible and are reported. Locally
retained journal revisions restore the Journal pane and transcript, with completion
cards reconstructed at recorded successful turn boundaries for acknowledged reports.
Missing journal records or turn timing cannot restore those cards. Original
keystrokes, window sizes, and live diff previews are not reconstructed; file-change
activity is retained.
Unreadable journal records are reported without blocking recorded host activity.

The playback bar shows position, speed, and simulated-streaming status. **Space**
pauses, **+/-** steps through speed presets from 0.1x to 100x, **[/]** seeks ten
seconds, **r** restarts, **Ctrl-B 1–5** selects Main, Diff, Activity, Agents, or
Journal, **j/k** or the mouse wheel scrolls Main, and **q** quits.
Speed presets include 1x in both directions, even after reaching either limit;
a custom `--speed` moves to the next preset in the selected direction. Playback
stops on its final frame until you quit.
Use `--from 5m --until 6m` to review a recorded interval; earlier state is loaded
first. The current terminal size controls layout.

For profiling without a terminal:

```sh
mekugi replay-session --session SESSION_ID --headless \
  --width 160 --height 48 --cpu-profile /tmp/replay-cpu.pprof \
  --heap-profile /tmp/replay-heap.pprof
go tool pprof -top /tmp/replay-cpu.pprof
```

Profile destinations must not already exist. Headless playback still lays out and
paints the UI, but discards terminal bytes; it cannot measure terminal backpressure.
Its final JSON reports frame timing percentiles, maximum frame time, and write
time. Profiles exclude initial input loading and state reconstruction before
`--from`, but include cold first-frame rendering. Seeking skips intermediate
paints and flushes completed output, so its initial output visibility and collapse
timing can differ from continuous playback. Use continuous playback to measure
steady-state behavior rather than treating a seek-start profile as warmed up.
Accelerated playback changes event batching per frame: use 1.0x for
representative wall-time latency, and the same seed, speed, dimensions, and inputs
for comparisons. Session contents remain local and may appear on screen.

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

For offline terminal-layout regression checks, run `make test-ui-snapshots`.
Failures leave `.txt.new` candidates beside the reviewed fixtures and print a
diff without replacing the baseline. After reviewing an intentional change,
run `make update-ui-snapshots SNAPSHOT='^TestUISnapshotJournalReply$'`, then
rerun the check. Omitting `SNAPSHOT` updates all matching cases. See
[terminal UI snapshot testing](CONTEXT-TESTS.md#terminal-ui-snapshots) for fixture
locations and coverage limits.

Bun is required to regenerate and test plugin assets:

```sh
go generate ./internal/router/toolplugin
bun test ./internal/router/toolplugin/tests
go test ./...
go vet ./...
```

## License

MIT. See [LICENSE](LICENSE).
