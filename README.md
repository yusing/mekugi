# mekugi

A mekugi is the small peg that pins a Japanese sword's handle to the blade.
Take it out and the handle comes off. Leave it in and the blade is still the
blade.

Mekugi adds compact, recoverable tools and live subagent activity to stock
Codex. Codex keeps editing, execution, the sandbox, permissions, command
sessions, and patch review. No fork, no config edits, no daemon.

[Features](#features) · [Install](#install) · [Usage](#usage) ·
[Metrics](#metrics) · [Settings](#mekugi-settings) · [Troubleshooting](#configuration-and-troubleshooting) · [Documentation](#documentation)

## Features

### UX

- **Keep the familiar Codex workflow.** No persistent service or configuration
  edits. Codex keeps control of permissions, execution, and patch review.
- **See subagent activity as it happens.** Start notices include the spawn prompt.
  Progress, messages, and replies appear in the main conversation, with each agent
  identified. Some updates wait for the next main-agent response; encrypted messages
  stay private. In Herdr, the [agents pane](#agents-pane) takes over this activity
  and shows it live, beside a roster of every agent.
- **Follow milestones, not another task list.** Agents keep a revisable journal
  that shows live updates and groups answers at completion.
- **Watch commands and file changes live.** Herdr's [live diff pane](#live-diff-pane)
  streams main-agent and subagent stock tool calls before completion, then
  switches to observed edits, with pause and flush controls.
- **See usage and cost.** Main completion shows one table of provider-reported tokens,
  input cache-hit rates, and estimated API costs per agent, plus a total. Child completion
  does not repeat the table. Costs are API estimates, not subscription charges;
  missing evidence is not presented as a complete total.
- **Inspect sessions in your browser.** A per-launch dashboard shows request
  metrics, token usage, and cache diagnostics.
- **Use other providers alongside OpenAI models.** Enable [Grok](#grok-models)
  with `--grok`, or configure [OpenCode Go or Zen](#opencode-go-and-zen) with an
  API key.

### AX (agent experience)

- **Use familiar stock tools.** Code Mode JavaScript, `tools.exec_command`, and
  `tools.apply_patch` keep their ordinary Codex behavior. Mekugi does not run an
  edit a second time.
- **Keep context after compaction.** A trusted Codex hook restores a bounded
  snapshot of the main thread's journal and recorded changes after compaction,
  without another model request. See [setup](#configuration-and-troubleshooting).
- **Resume running work.** Continue yielded processes through Codex's
  `write_stdin` handles instead of restarting them.
- **Recover omitted output.** Read captured overflow with `mread` without
  rerunning the command or reader that produced it.
- **Review completed edits.** `mchanges` tracks observed `apply_patch` results
  and the file effects of shell commands such as redirects, `cp`, `mv`, `rm`,
  and `sed -i`, interpreter writes, VCS operations, and formatters, including
  partial outcomes and explicitly labeled gaps in coverage.

### Round-trip and token saving

- **Batch work in Code Mode.** Use JavaScript to group related stock tool calls;
  `Promise.all` can run independent calls in parallel. `mcat` batches file reads
  under one output budget.
- **Read less source.** Bounded reads, semantic references, and structural
  outlines keep irrelevant source out of the model's context.
- **Review only relevant changes.** Compact change IDs scope review output
  instead of requiring the entire Git diff.
- **Hand off child changes automatically.** Child completion results include
  retained change ranges and aggregated line counts for focused parent review.
- **Finish with a journal update.** A `functions.journal` finish can deliver the
  final report without another model request just to restate it.

### Agent performance

- **Start with a mentor, then hand back.** [Mentor Handoff](doc/spec/mentor.md)
  lets eligible threads begin on a stronger model before returning to their
  configured model. It is enabled by default, with independent
  [controls](#options) for main sessions and subagents.

Fewer model round trips, token savings, and model handoffs do not guarantee faster
commands or better results on every task. For controlled comparisons, use
[codex-setup-ab](https://github.com/yusing/codex-setup-ab).

## Install

### Requirements

- **Go 1.27+**, CGO enabled, and a C toolchain to build the binary.
- **Codex CLI**, signed in with `codex login` using ChatGPT authentication.
- **Node.js 24+** available as `node`, and **ripgrep** available as `rg` on
  the router's `PATH` for mekugi mode.
- Any interpreter your agent selects, such as `python3`, on the executor's
  `PATH`.

Install the router:

```sh
go install github.com/yusing/mekugi/cmd/mekugi@latest
```

Add `$GOBIN`, or `$(go env GOPATH)/bin` when unset, to your `PATH`.

Then launch:

```sh
codex login
mekugi codex
```

Mekugi prints a dashboard URL before Codex opens. Use Codex as usual; the router
projects its tools and additive journal guidance automatically.

### From a checkout

With **Bun** and **Make** installed:

```sh
make install
```

This regenerates the embedded plugins and installs the router binary. Installation
and uninstallation leave Codex configuration and instruction files untouched.
`make uninstall` removes only the installed `mekugi` binary. Running sessions
retain their own worker executable; start a new session to use an update.

## Usage

Mekugi keeps private replay records on disk so resumed and forked conversations
retain their original tool history. These records include observed tool inputs
and change evidence, not just metrics. See [replay storage](#replay-storage) for
location, limits, and cleanup.

Put Mekugi flags **before** `codex`; arguments after it belong to Codex:

```sh
mekugi codex
mekugi codex --model gpt-6-astra
mekugi codex --model gpt-6-sol
mekugi codex --model gpt-6-luna
mekugi codex exec "Explain this repository"
mekugi codex resume 'CONVERSATION_ID'
mekugi --mentor-handoff=false codex
```

Each invocation starts a private router on a random loopback port and shuts it
down when Codex exits. Multiple sessions can run independently. Codex handles
terminal Ctrl-C after launch, and its exit status is preserved. During startup,
Ctrl-C cancels preparation without launching Codex.

Legacy `gpt-5.6-terra` selections route to `gpt-6-sol`; use `gpt-6-sol`
directly for new sessions.

The wrapper uses the fixed Codex ChatGPT upstream and overrides provider
selection for that invocation only. Standalone serving, fixed ports, custom
provider endpoints, and provider-selection arguments such as `--oss` are not supported.
The built-in Grok and OpenCode routes use their own authentication.
It also forces `include_collaboration_mode_instructions=false` for the invocation,
so Codex does not inject collaboration-mode instructions, even if enabled in your
config or command-line overrides. No configuration files are changed.

The wrapper enables WebSockets between Codex and Mekugi for that invocation,
without changing Codex configuration. Mekugi keeps the ChatGPT connection open
across responses so a compatible Codex client can send
[mid-turn steering](https://developers.openai.com/api/docs/guides/steering)
updates. Steering requires a supporting client and model; enabling the transport
does not add steering to an older Codex client.

Networks must allow secure WebSocket connections to ChatGPT. Mekugi also accepts
HTTP/SSE clients and can fall back to HTTP for those requests when ChatGPT
explicitly rejects the WebSocket upgrade. It never silently replays a dropped
request or accepted steering. Grok and OpenCode provider requests remain on HTTP.

### Options

| Flag | Default | Purpose |
| --- | --- | --- |
| `--mode` | `mekugi` | Use `passthrough` to forward traffic without mekugi tools, plugins, or Mentor Handoff |
| `--main-mentor-handoff` | `true` | Enable mentor handoff for eligible main sessions and ordinary forks |
| `--mentor-handoff` | `true` | Use `false` to keep subagents on their configured models |
| `--grok` | `false` | Enable Grok models in mekugi mode |
| `--grok-auth-file` | `~/.grok/auth.json` | Select a Grok OAuth credential store |
| `--timeout` | `10m` | Wait for the upstream response to start |
| `--stream-idle-timeout` | `4m` | Limit gaps between provider messages during an active response, or HTTP response bytes |
| `--capture-output PATH` | Disabled | Append sanitized JSONL metrics |
| `--metrics-output PATH` | Disabled | Write the final metrics snapshot on shutdown, overwriting the destination |
| `--debug` | Disabled | Record diagnostics, capture, metrics, forwarded instruction/tool snapshots, runtime reads, and an AX report; print all artifact paths on exit |

For a transport-only session:

```sh
mekugi --mode passthrough codex
```

Passthrough does not load the plugin registry, so it does not require Node.js or
plugin grammar validation. Capture remains available.

### Grok models

Opting in enables Grok in the model picker. Authenticate with `grok login --oauth`,
or supply `XAI_API_KEY` in the router's environment. An API key takes precedence.
Codex credentials are never forwarded to Grok.

```sh
mekugi --grok codex
mekugi --grok codex -m grok:grok-4.7
```

Ask the main agent to spawn `grok:grok-4.5`, `grok:grok-4.6`, `grok:grok-4.7`,
or `grok:grok-4.7-build-fast` in fresh context (`fork_turns="none"`). The Fast
variant requires Grok OAuth; it is unavailable with `XAI_API_KEY`.
Codex still manages the child, tools, permissions, and follow-ups. Switching an
existing OpenAI conversation still requires history that Grok can read; encrypted
OpenAI history remains unsupported. Grok sessions also have
[catalog restrictions](#third-party-model-catalogs). See the
[Grok model requirements](doc/spec/grok.md) for catalog, search, and
token-limit behavior.

### OpenCode Go and Zen

Set an API key, or use [Mekugi settings](#mekugi-settings).

```sh
export OPENCODE_GO_API_KEY='your-key'
mekugi codex -m opencode-go:glm-5.3
```

```sh
export OPENCODE_ZEN_API_KEY='your-key'
mekugi codex -m opencode-zen:kimi-k3
```

### Third-party model catalogs

When Grok or an OpenCode service is enabled, Mekugi adds its models to Codex's
model catalog for that invocation. This cannot be combined with Codex's named
`--profile` option or `exec --ignore-user-config`. Use the default configuration
or an explicit `-c model_catalog_json=...` instead.

## Editing and execution

Codex owns editing and execution. Use its stock `apply_patch` and
`exec_command` tools directly, or call them from Code Mode JavaScript. Mekugi
passes their arguments and results through unchanged. For independent work, a
Code Mode cell can use `Promise.all` to run calls in parallel:

```js
const results = await Promise.all([
  tools.exec_command({cmd: "rg -n 'TODO' src"}),
  tools.exec_command({cmd: "mcat README.md 1:80"}),
]);
for (const result of results) text(result.output);
```

Run ordinary shell commands or an interpreter through `exec_command`. For a
long-running command, use Codex's returned session ID with `write_stdin`; Mekugi
does not create a second process handle. A `cat > path <<'EOF'` command can be
previewed while it streams. Durable `mchanges` evidence comes from a finalized
stock `apply_patch` call or observed command effects. Derived scopes retain before
content; bounded sweeps report additional changes without inventing baselines.
Literal Python and JavaScript writes can also be previewed. Running cards show
scoped changes observed so far, not a claim that execution succeeded.

### Wrapped-session helpers

The commands below are session-private executables on Codex's `PATH`, not global
utilities. They use the same authenticated pinned tool snapshot as configured
plugins. Invoke them through stock `exec_command`. Mekugi also shows their
snapshot-owned descriptions to the agent in the execution tool guidance.

| Command | Purpose | Extra prerequisite on the executor's `PATH` |
| --- | --- | --- |
| `mread` | Continue bounded retained output by reference | Access to the router's replay directory |
| `mrun` | Bound a foreground command's output and optionally keep its ending | The wrapped command |
| `mchanges` | List the current agent thread's change IDs, read observed patches and command effects by ID or range, or revert and reapply them | Access to the router's replay directory |
| `mcat` | Read raw UTF-8 rows, with multi-file batching, ranges, and tail selection | None |
| `msymbol` | Look up definitions and references as `"PATH":LINE TEXT` rows | `gopls` for Go; TypeScript 7 as `tsc` for JS, TS, and JSON; `pyright-langserver` for Python |
| `inspect_file` | Inspect a structural outline | None |

Agent-facing change and output references use short word handles. The root and
its subagents share a namespace; `/fork` and `/side` copy visible state once,
then allocate independently. Resuming keeps retained references. For example:

```sh
mchanges --list
mchanges amber1..amber3 --summary
mchanges amber2 --history
mchanges revert amber2
mchanges apply amber2
mread REF
mread REF_A REF_B
mcat source.ts 10-20 40:60
mcat --tail -n 20 source.ts
mrun --tail -n 20 -- go test ./internal/router
```

`mchanges --summary` gives tab-separated added/deleted line counts per path.
These counts are summed across selected observed records, not a current Git
diff. Tool-managed files have a separate group; unknown counts stay unknown.
Use `--history` or an explicit path filter to expand managed diffs.
Bounded output includes an exact `mread` continuation when needed. Multiple handles
share one budget. `mcat` accepts colon or dash ranges and several ranges per path;
`-n` retains its default 6000-token ceiling, with omitted rows recoverable through `mread`.
`mchanges --list` shows the current thread's pending and completed IDs. A
Code Mode patch may show observed file changes as application unconfirmed:
completion of the outer JavaScript cell is not proof that its nested patch
succeeded. From a subdirectory, `--workspace ..` selects the parent workspace's
change index; paths after `--` only filter files within a selected change.
`mchanges revert` undoes selected changes in the workspace and `mchanges apply`
replays them. As with `git revert`, edits made since then are merged, and
overlapping ones leave conflict markers. Each touched file is reported relative
to the recorded change history rather than Git: `clean` when every recorded
change to it is undone, or its net `+N -N` rows. A revert is recorded as a new
change, so it can be undone too.
See the [change record](doc/spec/changes.md), [reader](doc/spec/read.md), and
[execution contract](doc/spec/execution.md).

### Live diff pane

In an interactive Herdr pane with `herdr` on `PATH`, `mekugi codex` launches
the live diff viewer when it first observes editing or execution. Read-only
turns do not open a pane.

The pane opens in stream view. Concurrent main-agent and subagent stock calls
get separate labeled cards; input appears as it arrives, before each call
completes. `apply_patch` input and stock `cat` heredoc writes can show
provisional file diffs. Completed `apply_patch` results and workspace outcomes
supply the saved diff view. A failed or unfinished call never becomes a
successful change record.

For Code Mode batches, the live input card displays literal `tools.exec_command`
commands as Bash while they arrive. Numbered `# tools.exec_command N` headers
separate calls; multiline commands stay under one header.

The viewer switches to the saved diff when a root turn's token metrics and
journal flush arrive, then back to stream for the next prompt. Press `v` to
switch manually. Previews remain provisional until application is confirmed.

- `v` switches between stream (the default) and diff views.
- In diff view, `j`/`k` scroll, `Space`/`b` page, `g`/`G` jump to the top or
  bottom, and `n`/`p` switch files. Each pauses automatic following.
- In diff view, `r` resumes following new changes.
- In diff view, `f` flushes the current file; `F` flushes all files.
- `q` quits the viewer without ending Codex.

Use `mchanges` for saved capture history. See [live view details](doc/spec/changes.md#live-terminal-view).

### Agents pane

In the same Herdr setup, the first subagent event opens a **Mekugi agents** pane.
Child progress, messages, and replies stream there as they are observed, including
during a native wait, instead of waiting for the next main-agent response. The main
conversation gets one notice when activity moves to the pane and another if it
returns inline. Usage tables and critical session notices stay in the main
conversation. Only the first root conversation with subagents uses the pane.

The pane shows a tree of agents with each one's current activity and age, beside
or above a feed grouped by agent. Roster markers are observed facts, not
completion claims: `◐` an open provider response, `!` a latest error, `✓` a
final answer sent, and `·` otherwise. Journal results appear as questions and
answers with each agent's recorded change totals. An agent keeps the same color
in both panes.

If the pane does not attach within 15 seconds, is closed, or stays disconnected
for more than 5 seconds, activity returns to the main conversation for the rest of
that session. Without Herdr, delivery stays inline.

- `n`/`Tab` and `p` select the next or previous agent; `o` shows only that agent.
- `j`/`k` scroll, `Space`/`b` page, and `g` jumps to the top.
- `r` or `G` resumes following new activity.
- `q` closes the pane without ending Codex.

See the [agents pane contract](doc/spec/commentary.md).

## Metrics

Open the dashboard URL printed at startup. It belongs to that session and stops
working when Codex exits. For an SSH session, forward its assigned port first.
The dashboard's **Transport** column is the provider connection (WebSocket or
HTTP), not the Codex-to-mekugi HTTP/SSE connection.

```sh
curl -sS "${MEKUGI_BASE_URL%/v1}/api/metrics"
mekugi --capture-output capture.jsonl --metrics-output metrics.json codex
```

Metrics stay in memory unless you request an export. Capture appends JSONL; the
final snapshot overwrites its destination. Exports contain sanitized
measurements, not raw prompts, scripts, patches, or credentials.
Provider-reported usage is authoritative; local token estimates are not billing
figures. See the [metrics reference](doc/spec/metrics.md).

To record diagnostics and forwarded request instructions for new requests:

```sh
mekugi --debug codex
```

Debug mode writes a private `mekugi-debug-*` directory in the system temporary
directory and prints its artifact paths on exit. Forwarded instruction dumps are not
sanitized and can contain private information from your instructions. Existing
`--capture-output` and `--metrics-output` paths take precedence. Resuming with
`--debug` records future requests; it cannot recover an earlier request that was
not dumped. See the [feature evidence contract](doc/spec/router.md#feature-usage-debug-evidence).

## Mekugi settings

Create `mekugi/config.toml` beneath your user configuration directory:
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

Use any of these sections independently. `OPENCODE_API_KEY` overrides both file keys;
`OPENCODE_GO_API_KEY` and `OPENCODE_ZEN_API_KEY` override their respective service.
An explicitly empty environment value disables that service. Settings are read
at startup; Mekugi never rewrites this file or Codex's configuration.

Service-tier overrides replace the request's `service_tier` after model selection
(including Mentor Handoff), before forwarding upstream. Models not listed keep
their requested tier. Accepted values are `auto`, `default`, `fast`, `priority`,
and `flex`; `fast` is mapped to `priority` for forwarding. Both aliases display as
`fast`, including when the tier comes from the request. The selected provider must support the tier.
Agent-start commentary shows the effective requested tier, not a guarantee of the
tier the provider will serve.

OpenCode models, API formats, reasoning controls and prices refresh online with
an hourly cache. Supported reasoning efforts are selectable normally; no `none`
override is required. The model picker updates on the next launch. See the
[provider contract](doc/spec/opencode.md) for cache behavior,
supported APIs and history limitations.

## Configuration and troubleshooting

- **Post-compaction recovery:** in Mekugi mode, the wrapper registers a native
  Codex `SessionStart` hook matching `compact`. Open `/hooks` in Codex and trust
  the Mekugi `post-compact` command before using it. After manual or automatic
  compaction, it restores a bounded snapshot of the main thread's journal and
  recorded change summary before the next model request. Subagents and
  passthrough mode are unchanged. Requires Codex's compact `SessionStart` support
  (validated with CLI 0.156.1); disabling native hooks disables recovery.
  Existing user/project hooks remain loaded, and Mekugi does not change your
  Codex configuration files or bypass hook trust. If you supply explicit
  `hooks` configuration through CLI `-c`, Mekugi leaves it untouched and prints
  a notice instead of registering its hook. Add a `SessionStart` command handler
  matching `^compact$`, with command `/absolute/path/to/mekugi post-compact`, to
  your hook configuration in that case. Quote the executable path if it contains
  spaces. Hook failures are advisory and do not stop the task.
- **Instructions:** Mekugi preserves Codex's stock or caller-configured base instructions. It adds
  journal and finish guidance through the projected tool descriptions without editing instruction
  files. See [guidance behavior](doc/spec/guide.md).
- **Plugins:** put regular `.js` or `.mjs` modules in `mekugi/plugins` beneath
  your platform's user configuration directory. On Linux this is
  `$XDG_CONFIG_HOME/mekugi/plugins` or `~/.config/mekugi/plugins`; on macOS it is
  `~/Library/Application Support/mekugi/plugins`. See the
  [plugin contract](doc/spec/plugin.md).
- **Executor environment:** the router and executor must see the same workspace
  paths and tool runtime directory. `MEKUGI_RUNTIME_DIR` overrides the default
  temporary directory; both must resolve it to the same absolute path.
- **Failures:** startup errors appear before Codex launches. Session failures
  appear as user-only commentary; undelivered notices appear on stderr after
  Codex exits. A router translation fault ends the turn instead of repeatedly retrying;
  its notice offers recovery steps. Sanitized failure references survive restart,
  without storing request content. Detailed logs still require `--debug`.
  See [opt-in agent issue reports](doc/spec/diagnose.md).

### Replay storage

Replay records live at `$XDG_STATE_HOME/mekugi/replay`, or
`~/.local/state/mekugi/replay` when `XDG_STATE_HOME` is unset. Resuming or
opening a side conversation needs no extra Mekugi flag. Replay does not rerun
old commands or restore live processes.

Mekugi removes its session data after **14 days without activity**. When storage
fills, it removes the least recently active inactive sessions until the new data
fits. Running work is protected. **Your original Codex chats and workspace files
are never deleted.** Age-based cleanup runs in the background rather than as
part of a new request. Cleanup can make old recovery and review references stop
working. To reset storage, stop all Mekugi wrappers and move the replay
directory aside.

### Inspect a session

Inspect a local Codex rollout without running old commands. This is read-only
and starts no router:

```sh
mekugi inspect-session --session /path/to/rollout.jsonl
mekugi inspect-session --failures
mekugi inspect-session --failures e8bd3ee1d49f
mekugi inspect-sessions --exclude-model '*grok*' --class production
MEKUGI_AX_OUTPUT=/path/to/private/reads.jsonl mekugi --debug codex
mekugi inspect-session --session /path/to/rollout.jsonl --ax \
  --read-log /path/to/private/reads.jsonl
```

Default JSON contains logical tool names, call IDs, outcomes, and sizes, not
private text. Requested `--field` values may contain source and command output.
Reread candidates are not proof of waste. See the
[session inspection](doc/spec/session.md) and [AX evidence](doc/spec/ax.md)
contracts.

### Older installations

Finish active sessions before replacing an older installation. Retire any old
service and provider configuration separately, preserving unrelated settings and
authentication. Use `mekugi codex` for future sessions.

## Go library

The root package provides review-diff rendering helpers used by the router's
change evidence. Editing and execution are Codex-owned, not root-library APIs.

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

For focused checks, use `go test .` for review rendering,
`go test ./internal/router` for routing, or
`go test ./cmd/mekugi` for the process entry point.

## License

MIT. See [LICENSE](LICENSE).
