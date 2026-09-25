# mekugi

A mekugi is the small peg that pins a Japanese sword's handle to the blade.
Take it out and the handle comes off. Leave it in and the blade is still the
blade.

Mekugi adds compact, recoverable tools and live subagent activity to stock
Codex. Codex keeps editing, execution, the sandbox, permissions, command
sessions, and patch review. No fork, no config edits, no daemon.

[Features](#features) · [Install](#install) · [Usage](#usage) ·
[Metrics](#metrics) · [Settings](#mekugi-settings) ·
[Troubleshooting](#configuration-and-troubleshooting) · [Documentation](#documentation)

## Features

- **Stock Codex workflow.** Code Mode JavaScript, `exec_command`, and
  `apply_patch` behave as usual. Mekugi never re-runs an edit and changes no
  configuration or instruction files. A persistent WebSocket lets a supporting
  Codex client [steer a running turn](#usage).
- **Milestone journal.** Agents keep a revisable journal. You see live updates,
  and answers are grouped at completion. A final answer goes into the journal
  without an extra model request.
- **Live subagent activity.** Start notices show model and effort. Progress and
  message excerpts appear in the main conversation, or live in Mekugi’s
  [agents pane](#agents-pane). Encrypted messages stay private.
- **Live diffs.** Mekugi’s [live diff pane](#live-diff-pane) streams tool calls
  and provisional diffs as they arrive, then shows the saved edits.
- **Recoverable output and change review.** [Session helpers](#wrapped-session-helpers)
  continue truncated output without rerunning, and track edits from
  `apply_patch` and shell commands, with any gaps in coverage labeled. Edits can
  be reverted and reapplied by ID.
- **Fewer tokens and round trips.** Bounded and batched reads, semantic symbol
  lookup, structural outlines, scoped change IDs, and child change handoffs.
- **Search output filtering.** With a TypeSafe API key configured, large search results
  (`rg`, `grep`, `find`, `fd`, `git grep`), linter and compiler diagnostics,
  `git log`, `git diff`, `git show`, and `--help` pages drop the files, commits,
  or entries that TypeSafe's Jev model judges unrelated to the task, including
  inside combined commands such as `rg … | head; mcat …`. The agent sees what
  was omitted and can `mread` the full output. Your
  latest request, the agent's preceding message, the command, and sampled result
  rows are sent to TypeSafe. If TypeSafe fails, the output passes through unchanged.
  Opt out with `--explore-filter=false`.
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
- **Usage and cost.** Eligible main completions update a Markdown usage snapshot. A per-launch
  browser dashboard shows request metrics and cache diagnostics. Costs are API
  estimates, not subscription charges.
- **Diagnostics.** [Inspect past sessions](#inspect-a-session) offline, record
  debug evidence, or let agents [report issues](#configuration-and-troubleshooting)
  to your own command.
- **Mentor Handoff.** [Eligible threads](doc/spec/mentor.md) start on a stronger
  model and then return to their configured one. It is on by default.
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
go install github.com/yusing/mekugi/cmd/mekugi@latest
codex login
mekugi codex
```

Add `$GOBIN`, or `$(go env GOPATH)/bin` if that is unset, to your `PATH`. Mekugi
prints a dashboard URL, then opens Codex. Use Codex as usual.

From a checkout with **Bun** and **Make**, run `make install`. It regenerates the
embedded plugins and installs the binary. `make uninstall` removes only that
binary. Running sessions keep their worker executable, so start a new session
to pick up an update.

## Usage

Mekugi flags go **before** `codex`; everything after `codex` goes to Codex:

```sh
mekugi codex --model gpt-6-sol
mekugi codex exec "Explain this repository"
mekugi codex resume 'CONVERSATION_ID'
mekugi --mentor-handoff=false codex
```

Each invocation:

- starts a private router on a random loopback port and stops it when Codex
  exits. Independent sessions can run side by side. Codex handles Ctrl-C after
  launch and keeps its exit status. During startup, Ctrl-C cancels without
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

### Options

| Flag | Default | Purpose |
| --- | --- | --- |
| `--mode` | `mekugi` | Use `passthrough` to forward traffic without mekugi tools, plugins, or Mentor Handoff |
| `--main-mentor-handoff` | `true` | Enable mentor handoff for eligible main sessions and ordinary forks |
| `--mentor-handoff` | `true` | Use `false` to keep subagents on their configured models |
| `--post-compact-recovery` | `true` | Use `false` to skip the post-compaction context hook |
| `--grok` | `false` | Enable Grok models in mekugi mode |
| `--grok-auth-file` | `~/.grok/auth.json` | Select a Grok OAuth credential store |
| `--explore-filter` | `true` with a TypeSafe API key | Use `false` to keep search output unfiltered. See [search output filtering](#features) |
| `--timeout` | `10m` | Wait for the upstream response to start |
| `--stream-idle-timeout` | `4m` | Limit gaps between provider messages during an active response, or HTTP response bytes |
| `--capture-output PATH` | Disabled | Append sanitized JSONL metrics |
| `--debug` | Disabled | Record diagnostics, capture, metrics, forwarded instruction/tool snapshots, runtime reads, and an AX report; print all artifact paths on exit |

`mekugi --mode passthrough codex` forwards traffic only. It doesn't need Node.js,
and capture still works.

### Grok models

Authenticate with `grok login --oauth`, or set `XAI_API_KEY` in the router's
environment; an API key takes precedence. Codex credentials are never sent to
Grok.

```sh
mekugi --grok codex -m grok:grok-4.7
```

Subagents can use `grok:grok-4.5`, `grok:grok-4.6`, `grok:grok-4.7`, or
`grok:grok-4.7-build-fast` (OAuth only) in fresh context (`fork_turns="none"`).
Grok can't read encrypted OpenAI history, so switching an existing OpenAI
conversation to Grok isn't supported. See the [Grok requirements](doc/spec/grok.md).

### OpenCode Go and Zen

Set an API key here or in [Mekugi settings](#mekugi-settings):

```sh
OPENCODE_GO_API_KEY='your-key' mekugi codex -m opencode-go:glm-5.3
OPENCODE_ZEN_API_KEY='your-key' mekugi codex -m opencode-zen:kimi-k3
```

Models, reasoning controls, and prices refresh from an hourly cache. The model
picker updates on the next launch. See the [provider contract](doc/spec/opencode.md).

Grok and OpenCode requests use HTTP and their own authentication. Their models
are added to Codex's catalog for the invocation. That can't be combined with
`--profile` or `exec --ignore-user-config`; use the default configuration or an
explicit `-c model_catalog_json=...` instead.

## Editing and execution

Codex owns editing and execution. Mekugi passes stock `apply_patch` and
`exec_command` arguments and results through unchanged. In Code Mode,
`Promise.all` runs independent calls in parallel:

```js
const results = await Promise.all([
  tools.exec_command({cmd: "rg -n 'TODO' src"}),
  tools.exec_command({cmd: "mcat README.md 1:80"}),
]);
for (const result of results) text(result.output);
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

### Live diff pane

In an interactive terminal, `mekugi codex` owns its layout without an external
pane manager. It opens a live diff viewer at the first edit or command. Read-only turns don't open it. Main-agent
and subagent calls get labeled cards that stream input as it arrives. When a
turn finishes, the viewer switches to the saved diff. A failed or unfinished
call never becomes a saved change.

- `Ctrl-B`, then `1`/`2`/`3`/`4`, focuses Codex, diffs, agents, or the roster. Click a pane to focus it.
- Drag the dividers to resize panes or the file navigator. `Ctrl-B`, then arrow
  keys, resizes the main splits (up/down in the roster adjusts its height); `Ctrl-B`, then `[`/`]`, resizes the file navigator.
  Narrow terminals show the focused pane full-width.
- `Ctrl-B`, then `PageUp`/`PageDown`, browses inline Codex history. The wheel
  does the same when Codex is not handling mouse events. Typing returns to live
  output. Up to 10,000 retained history rows are restored on exit.
- `v` switches views; `?` lists diff shortcuts. `Ctrl-C` in an auxiliary pane
  returns focus to Codex; in Codex it retains Codex’s normal behavior.
- `s` shows or hides the file tree, `t` toggles tree/flat paths, `/` filters files.
- `n`/`p` change files, `[`/`]` jump between hunks, and `j`/`k`, `Space`/`b`,
  and `g`/`G` scroll. Opening a file starts at its header.
- `Tab` switches the navigator to **Changes**: a graph of changes by caller
  (`main` or the agent's name) with each change's source, such as
  `apply_patch`, `sed`, or `python3`. `{`/`}` step through changes, `a` shows
  one caller's changes at a time, and `0` shows all callers. In the tab, `Enter`
  on a caller filters to it, and `Enter` or `h`/`l` on a change expands or
  collapses its files.
- Browsing pauses following; `r` resumes.

See [live view details](doc/spec/changes.md#live-terminal-view).

### Agents pane

The first provider response opens a **Mekugi agents** pane in the same terminal.
It streams main and child activity, messages, and replies, including
during a native wait. The separate roster spans the full width below Codex and the
diff, with a draggable divider. It gives `main` and each child one row: activity, then
age, `↑`/`↓` tokens, estimated cost to two decimal places, and turns in stable columns.
Roles color the existing status glyph; a legend sits beside the bottom status line
or on the row above when space is limited. Costs come from the same totals
as the usage table; `≥$N` means some response ended without usage. Markers show what
was observed: `◐` open response, `!` latest error, `✓` final answer sent. The selected
agent's row is shaded. Usage tables and critical notices stay in the main conversation.

- In the roster, `↑`/`↓` or `k`/`j` select all agents or an individual agent;
  `o` toggles the selected-agent filter. Roster rows are clickable. The mouse wheel
  scrolls the roster without changing the feed filter. Crowded rosters compact, with
  counts of hidden responding agents and errors above or below the visible rows.
- Scrolling matches the diff pane: `↑`/`↓` or `k`/`j` move one line,
  `PageUp`/`PageDown` or `b`/`Space` move one page, and `Home`/`End` or `g`/`G`
  go to the top/bottom. Scrolling pauses following; `r` resumes it.
- The mouse wheel scrolls diff and activity by three lines per event, without moving keyboard focus.
- Click a clipped snippet in the feed to expand it; click again to collapse it.
- `Ctrl-C` returns focus to Codex without ending it.

Redirected sessions keep ordinary Codex input/output and inline agent activity.
Inside Herdr, Mekugi advertises its wrapped Codex process through Herdr’s agent
hint and passes through Codex title/status signals, so agent detection still works.
Herdr is optional and does not control Mekugi’s internal panes.

See the [agents pane contract](doc/spec/commentary.md).

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

[typesafe]
api_key = "your-typesafe-key"
```

Every section is optional. Settings are read at startup and never rewritten.

- **Service tiers** replace the request's tier after model selection, Mentor
  Handoff included. The values are `auto`, `default`, `fast` (sent as
  `priority`), `priority`, and `flex`. The provider must support the tier you
  choose.
- **API keys:** `OPENCODE_API_KEY` overrides both file keys. The per-service
  variables override their own service. Setting one to an empty string turns
  that service off. `[typesafe].api_key` takes precedence over
  `TYPESAFE_API_KEY`; an explicitly empty file value disables filtering.

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
- **Instructions:** Mekugi keeps Codex's base instructions and adds its guidance
  through tool descriptions. Anything between `<!-- mekugi:omit -->` and
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

Bun is required to regenerate and test plugin assets:

```sh
go generate ./internal/router/toolplugin
bun test ./internal/router/toolplugin/tests
go test ./...
go vet ./...
```

## License

MIT. See [LICENSE](LICENSE).
