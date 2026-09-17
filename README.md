# mekugi

A mekugi is the small peg that pins a Japanese sword's handle to the blade.
Take it out and the handle comes off. Leave it in and the blade is still the
blade.

Mekugi pins compact agent tools onto stock Codex: hashline edits, direct
scripts, and inline subagent activity. Codex keeps the sandbox, permissions,
command sessions, and patch diff UI. No fork, no config edits, no daemon.

[Features](#features) · [Install](#install) · [Usage](#usage) ·
[Metrics](#metrics) · [Settings](#mekugi-settings) · [Troubleshooting](#configuration-and-troubleshooting) · [Documentation](#documentation)

## Features

### UX

- **Keep the familiar Codex workflow.** No persistent service or configuration
  edits. Codex keeps control of permissions, execution, and patch review.
- **See subagent activity inline.** Model notices, progress, messages, and replies
  appear in the main conversation, with each agent identified. Some updates wait
  for the next main-agent response; encrypted messages stay private.
- **Follow milestones, not another task list.** Agents keep a revisable journal
  that shows live updates and groups answers at completion.
- **Review edits as they happen.** Herdr's [live diff pane](#live-diff-pane)
  combines main-agent and subagent edits, with pause and flush controls.
- **See usage and cost.** Completion tables show provider-reported tokens and
  estimated API costs per agent, not subscription charges. Missing evidence is
  not presented as a complete total.
- **Inspect sessions in your browser.** A per-launch dashboard shows request
  metrics, token usage, compression, and cache diagnostics.
- **Use other providers alongside OpenAI models.** Enable [Grok](#grok-models)
  with `--grok`, or configure [OpenCode Go or Zen](#opencode-go-and-zen) with an
  API key.

### AX (agent experience)

- **Validate edits before application.** Related edits share one validation pass.
  Go edits are parsed and formatted; supported Python, JavaScript, and TypeScript
  edits get syntax checks and indentation correction. These checks do not replace tests.
- **Repair rejected edits without starting over.** Correct the retained script
  while preserving unrelated prepared changes.
- **Resume running work.** Continue yielded processes through Codex's
  continuation handles instead of restarting them.
- **Recover omitted output.** Read captured overflow without rerunning the
  command or search that produced it.

### Round-trip saving

- **Batch reads and commands.** Group related operations in one shell call.
  With Code Mode, batch separate programs, including different interpreters.
- **Run shell commands and patches in the same call.** With Code Mode,
  [combine shell commands and hpatch edits](#edits-and-shell-in-one-call), so
  preparation, editing, and validation do not need separate model turns.
- **Skip redundant source lookups.** Edit text already in context, reuse unchanged
  verified rows when uniquely identifiable, and use current references returned by
  successful edit reports.
- **Get repair context with the rejection.** Localized diagnostics can avoid a
  separate inspection call before correcting an edit.
- **Report progress within tool calls.** Record journal milestones alongside
  the work instead of making separate progress calls.
- **Finish without another model request.** A journal finish can deliver the
  final report with the last successful command, without another model turn
  just to write the response.

### Token saving

- **Omit repeated patch context.** Target verified rows, ranges, or exact text;
  write the replacement once and let Mekugi generate the patch framing.
- **Read less source.** Bounded searches, semantic references, and structural
  outlines keep irrelevant source out of the model's context.
- **Review only the relevant edits.** Compact change IDs scope review output
  instead of requiring the entire Git diff.
- **Write scripts without wrappers.** Direct scripts avoid wrapper code and
  extra quoting.
- **Compress repeated text.** Optional [CTP/2](doc/spec/ctp.md) losslessly encodes
  eligible model-visible text using local dictionaries and references. Tool names
  and new tool payloads stay native. Enable it with `--model-protocol ctp2`;
  it is off by default.
- **Summarize noisy command output.** If [RTK](https://github.com/rtk-ai/rtk) is on
  the executor's `PATH`, recognized display commands such as Git, Go, Cargo,
  JavaScript tooling, and search return compact summaries instead of full logs.
  Missing RTK leaves commands unchanged.

### Agent performance

- **Start with a mentor, then hand back.** [Mentor Handoff](doc/spec/mentor.md)
  lets eligible threads begin on a stronger model before returning to their
  configured model. It is enabled by default, with independent
  [controls](#options) for main sessions and subagents.

Fewer model round trips, token savings, and model handoffs do not guarantee faster
commands or better results on every task. See the [benchmark methodology](doc/benchmarks.md)
for comparisons.

## Install

### Requirements

- **Go 1.27+**, CGO enabled, and a C toolchain to build the binaries.
- **Codex CLI**, signed in with `codex login` using ChatGPT authentication.
- **Node.js 24+** available as `node`, and **ripgrep** available as `rg` on
  the router's `PATH` for mekugi mode.
- Any interpreter your agent selects, such as `python3`, on the executor's
  `PATH`. Bash and POSIX shell execution are built in.

Install both the router and its shell helper:

```sh
go install github.com/yusing/mekugi/cmd/mekugi@latest \
  github.com/yusing/mekugi/cmd/shell@latest
```

Add `$GOBIN`, or `$(go env GOPATH)/bin` when unset, to the `PATH` used by both
Mekugi and Codex. The fixed `shell` helper must be available to Codex's executor.

Then launch:

```sh
codex login
mekugi codex
```

Mekugi prints a dashboard URL before Codex opens. Use Codex as usual; the router
supplies the agent's tool guidance automatically.

### From a checkout

With **Bun** and **Make** installed:

```sh
make install
```

This regenerates the embedded plugins and installs both binaries. Installation
and uninstallation leave Codex configuration and instruction files untouched.
`make uninstall` removes only the installed `mekugi` and `shell` binaries.
Running sessions retain their own worker executable; start a new session to use
an installed update. For sessions started by older versions, follow
[the older-installation guidance](#older-installations) before replacing binaries.

## Usage

Mekugi keeps private replay records on disk so resumed and forked conversations
retain their original tool history. These records include tool inputs and recovery
diagnostics, not just metrics. See [replay storage](#replay-storage) for location,
limits, and cleanup.

Put Mekugi flags **before** `codex`; arguments after it belong to Codex:

```sh
mekugi codex
mekugi codex --model gpt-6-astra
mekugi codex exec "Explain this repository"
mekugi codex resume 'CONVERSATION_ID'
mekugi --model-protocol native --mentor-handoff=false codex
```

Each invocation starts a private router on a random loopback port and shuts it
down when Codex exits. Multiple sessions can run independently. Codex handles
terminal Ctrl-C after launch, and its exit status is preserved. During startup,
Ctrl-C cancels preparation without launching Codex.

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
| `--mode` | `mekugi` | Use `passthrough` to forward traffic without mekugi tools, plugins, CTP/2, or Mentor Handoff |
| `--model-protocol` | `native` | Use `ctp2` to enable CTP/2 in mekugi mode |
| `--main-mentor-handoff` | `true` | Enable mentor handoff for eligible main sessions and ordinary forks |
| `--mentor-handoff` | `true` | Use `false` to keep subagents on their configured models |
| `--grok` | `false` | Enable Grok models in mekugi mode |
| `--grok-auth-file` | `~/.grok/auth.json` | Select a Grok OAuth credential store |
| `--timeout` | `10m` | Wait for the upstream response to start |
| `--stream-idle-timeout` | `4m` | Limit gaps between provider messages during an active response, or HTTP response bytes |
| `--capture-output PATH` | Disabled | Append sanitized JSONL metrics |
| `--metrics-output PATH` | Disabled | Write the final metrics snapshot on shutdown, overwriting the destination |
| `--debug` | Disabled | Record diagnostics, capture, metrics, patched instructions, runtime reads, and an AX report; print all artifact paths on exit |

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
mekugi --grok codex -m grok:grok-4.6
```

Ask the main agent to spawn `grok:grok-4.6` in fresh context (`fork_turns="none"`).
Codex still manages the child, tools, permissions, and follow-ups. Switching an
existing OpenAI conversation still requires history that Grok can read; encrypted
OpenAI history remains unsupported.

`--grok` cannot be combined with Codex's named `--profile` option or
`exec --ignore-user-config`. Use the default configuration or an explicit
`-c model_catalog_json=...` instead. See the
[Grok model requirements](doc/spec/subagents.md) for catalog, search, and
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

## How editing and execution work

These are the agent tools Mekugi adds. Codex still authorizes every generated
patch and shell execution.

### Hashline edits

The agent selects a verified `LINE:HASH` target and sends the new text once.
Mekugi checks the script and generates the patch; Codex applies it. Supported
language checks run before application. Verification is not a workspace lock.
See the [editing guarantees](doc/spec/output.md) and
[target selection rules](doc/spec/select.md).

### Edits and shell in one call

With Code Mode, `functions.hpatch` can interleave atomic edit segments and shell
programs. Completed work stays applied if a later segment fails:

```text
shell test -f notes.txt
in notes.txt
type "draft" "ready"
shell rg -n ready notes.txt
```

Use ordinary hpatch for edits alone and the shell tool for command-only work.
Continue a yielded mixed script with `hpatch` using `resume HANDLE`, without
resending the original script. To fix code and retry the failed segment in one
call:

```text
resume HANDLE repair
in app.go
type "incorrect expression" "correct expression"
```

See the [mixed-script contract](doc/spec/script.md#shell-in-script).

### Direct scripts

The agent can send a program directly to `functions.shell`:

```python
#!python3
print("hello")
```

Bash is the default. Interactive and long-running programs still use Codex's
native execution and session facilities. Eligible literal `cat` heredoc writes
are converted to patches so they appear in the usual diff UI.

Commands that share an interpreter and execution options belong in one
multiline script. Independent programs can use an explicit sequential batch:

```text
#!batch=NEXT_PROGRAM
#!params={"yield_time_ms":1000}
echo hello
NEXT_PROGRAM
#!python3
print("hello")
```

Bash and POSIX scripts can record journal milestones on the current call, for
example `journal add 'Checked the inputs.' --report-now`. A final
`journal finish` can complete the turn without another model request. See the
[shell contract](doc/spec/shell.md) and [journal contract](doc/spec/journal.md).

### Command output summaries

When [RTK](#token-saving) is available, recognized display commands return
compact summaries. With `hrun`, RTK summarizes first, then `hrun` applies its
output limit. Private readers, pipelines, redirected output, command
substitutions, machine-readable formats, terminal-backed commands, and native
`find`/`diff` stay raw. Use an explicit executable path when a supported command
needs raw output.

### Shell helpers

The following commands are available **inside the tool's Bash and POSIX
programs**, not as standalone utilities in your terminal:

| Command | Purpose | Extra prerequisite on the executor's `PATH` |
| --- | --- | --- |
| `hrun` | Bound an external command's output, optionally keeping its ending | The wrapped command |
| `hchanges` | Read hpatch diffs and recovery history by ID or range | Access to the router's replay directory |
| `hcat` | Read verified source rows; use `--batch` first for shared-budget multi-file previews and per-file recovery links | Replay-directory access for batch mode |
| `hgrep` | Search text with verified row references | `rg` |
| `hsymbol` | Look up definitions and references | `gopls` for Go; TypeScript 7 as `tsc` for JS, TS, and JSON; `pyright-langserver` for Python |
| `inspect_file` | Inspect a structural outline | None |

Agent-facing references use short word handles such as `maple` or `amber1`.
Copy the emitted handle; existing references keep their original lifetime and
scope.

Hpatch keeps durable review records in the router's replay store. An agent can
hand off `amber1..amber3`, then another agent can retrieve just those edits:

```sh
hchanges amber1..amber3
hchanges amber1..amber3 --summary
hchanges amber2 --history
```

```text
amber1 applied
 src/parser.go | 11 ++++++++---
 1 file changed, 8 insertions(+), 3 deletions(-)
```

These are hpatch's evaluated changes, including formatting, not a record of
shell edits or other workspace changes. Incomplete reads return an exact
`hread REF` next call. Typical follow-ups:

```sh
hread REF
hsymbol def source.go 42 MyFunction
inspect_file source.go
hcat --tail -n 20 source.ts
hrun --tail -n 20 -- go test ./internal/router
```

Use an ordinary script file for source you need to edit or run repeatedly. See the
[change record](doc/spec/changes.md), [reader](doc/spec/read.md), and
[shell](doc/spec/shell.md) contracts for flags, bounds, and recovery.

### Live diff pane

In an interactive Herdr pane with `herdr` on `PATH`, `mekugi codex` opens a live
diff pane to the right on the first hpatch call, without changing focus.
Read-only turns and redirected input/output do not open a pane. The view
combines main-agent and subagent file edits, excluding Git and shell changes.
Streaming previews are provisional until application is reported.

To try the same UI without Codex or Herdr:

```sh
mekugi live-diff --simulate
mekugi live-diff --simulate --speed 2 --repeat
```

The simulation uses disposable temporary files and removes them on exit.

- `j`/`k` scroll and `n`/`p` switch files, pausing automatic following.
- `r` resumes following new edits.
- `f` flushes the current file; `F` flushes all files.
- `q` quits the viewer without ending Codex.

Use `hchanges` for saved capture history; standalone live viewing is not
supported. See [live view details](doc/spec/changes.md#live-terminal-view).

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

To record diagnostics and patched instructions for new requests:

```sh
mekugi --debug codex
```

Debug mode writes a private `mekugi-debug-*` directory in the system temporary
directory and prints its artifact paths on exit. Instruction dumps are not
sanitized and can contain private information from your instructions. Existing
`--capture-output` and `--metrics-output` paths take precedence. Resuming with
`--debug` records future requests; it cannot recover an earlier request that was
not dumped. See the [feature evidence contract](doc/spec/router.md#feature-usage-debug-evidence).

## Mekugi settings

Create `mekugi/config.toml` beneath your user configuration directory:
`$XDG_CONFIG_HOME` or `~/.config` on Linux, `~/Library/Application Support` on macOS.

```toml
[providers.opencode_go]
api_key = "your-go-key"

[providers.opencode_zen]
api_key = "your-zen-key"
```

Use either section or both. `OPENCODE_API_KEY` overrides both file keys;
`OPENCODE_GO_API_KEY` and `OPENCODE_ZEN_API_KEY` override their respective service.
An explicitly empty environment value disables that service. Settings are read
at startup; Mekugi never rewrites this file or Codex's configuration.

OpenCode models, API formats, reasoning controls and prices refresh online with
an hourly cache. Supported reasoning efforts are selectable normally; no `none`
override is required. The model picker updates on the next launch. See the
[provider contract](doc/spec/subagents.md#opencode-go-and-zen) for cache behavior,
supported APIs and history limitations.

## Configuration and troubleshooting

- **Custom instructions:** Mekugi supplies tool guidance in memory without
  editing your instruction file. If you use a custom prompt, configure it with
  Codex's `model_instructions_file` setting and restart Mekugi. See
  [guidance compatibility](doc/spec/guide.md).
- **Plugins:** put regular `.js` or `.mjs` modules in `mekugi/plugins` beneath
  your platform's user configuration directory. On Linux this is
  `$XDG_CONFIG_HOME/mekugi/plugins` or `~/.config/mekugi/plugins`; on macOS it is
  `~/Library/Application Support/mekugi/plugins`. See the
  [plugin contract](doc/spec/plugin.md).
- **Executor environment:** the router and executor must see the same workspace
  paths and shell runtime directory. `MEKUGI_RUNTIME_DIR` overrides the default
  temporary directory; both must resolve it to the same absolute path.
- **Failures:** startup errors appear before Codex launches. Session failures
  appear as user-only commentary; undelivered notices appear on stderr after
  Codex exits. Mekugi does not create operational log files unless `--debug` is
  enabled. See [opt-in agent issue reports](doc/spec/diagnose.md).

### Replay storage

Replay records live at `$XDG_STATE_HOME/mekugi/replay`, or
`~/.local/state/mekugi/replay` when `XDG_STATE_HOME` is unset. Resuming or
opening a side conversation needs no extra Mekugi flag. Replay does not rerun
old commands or restore live processes.

Mekugi removes its session data after **14 days without activity**. When storage
fills, it removes the least recently active inactive sessions until the new data
fits. Running work is protected. **Your original Codex chats and workspace files
are never deleted.** Cleanup can make old recovery and review references stop
working. To reset storage, stop all Mekugi wrappers and move the replay
directory aside.

### Inspect a session

Inspect a local Codex rollout without running old commands. This is read-only
and starts no router:

```sh
mekugi inspect-session --session /path/to/rollout.jsonl
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

The root package, `github.com/yusing/mekugi`, also exposes workspace evaluation,
application, reporting, and host translation APIs. See the
[workspace API requirements](doc/spec/file.md) and
[translation contract](doc/architecture/translate.md).

Library callers must coordinate concurrent writers. Multi-file installation is
not crash-atomic or isolated from readers, and an application error can follow
filesystem changes. Inspect the outcome before retrying; see the
[complete guarantees](doc/spec/output.md).

## Documentation

- [Product specification](doc/product.md)
- [Interface contracts](doc/spec/index.md)
- [Architecture ownership](doc/architecture/index.md)
- [Benchmark methodology](doc/benchmarks.md)
- [Codex end-to-end checks](doc/codex-router-e2e.md)

## Development

Bun is required to regenerate and test plugin assets:

```sh
go generate ./internal/router/toolplugin
bun test ./internal/router/toolplugin/tests
go test ./...
go vet ./...
make install
```

For focused checks, use `go test .` for the engine,
`go test ./internal/router` for routing, or
`go test ./cmd/mekugi ./cmd/shell` for process entry points.

## License

MIT. See [LICENSE](LICENSE).
