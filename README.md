# mekugi

A mekugi is the small peg that pins a Japanese sword's handle to the blade.
Take it out and the handle comes off. Leave it in and the blade is still the
blade.

Mekugi adds compact, recoverable tools and live subagent activity to stock
Codex. Codex keeps editing, execution, the sandbox, permissions, command
sessions, and patch review. No fork, no config edits, no daemon.

[Features](#features) · [Install](#install) · [Usage](#usage) ·
[UI](#ui) · [Metrics](#metrics) · [Configuration](#configuration) ·
[Documentation](#documentation)

## Features

### Agent-facing

- **Fewer tokens and round trips.** [Session helpers](#wrapped-session-helpers)
  offer bounded and batched reads, semantic symbol lookup, and structural outlines.
- **Recoverable output and changes.** Continue omitted output without rerunning,
  and review, revert, or reapply captured edits by ID. Gaps in coverage are labeled.
- **Leaner instructions.** [Omit marked instruction blocks](#troubleshooting-and-integrations)
  for the session without changing instruction files or unmarked policy.
- **Output deduplication.** Replace
  unchanged runs in tool output and attached file or skill content with references
  to earlier visible text by default in Mekugi mode. Full host results, retained evidence,
  and the UI stay intact. Transcript suffixes mark repeated output; `/session`
  shows occurrences and estimated saved tokens. Disable with
  `--deduplicate-output=false`;
  see the [projection contract](doc/spec/execution.md#duplicate-output-projection).
- **Session continuity.** Retained change evidence and output remain available across
  resume and forks, subject to [storage retention](#replay-storage).

### User-facing

- **Native terminal UI.** [Main, Diff, and Agents](#ui)
  share one terminal without an external pane manager.
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

- **Codex CLI** for tool execution in every [launch mode](#start-mekugi).
- Built-in frontends need neither Node.js nor Bun; semantic lookup requires the language servers listed under [agent-facing tools](#agent-facing).
- Any interpreter your agent picks, such as `python3`, on the executor's `PATH`.

### Release binaries

Download an archive from [GitHub Releases](https://github.com/yusing/mekugi/releases/latest).
Each archive includes `mekugi` and `mekugi-exec`; Go and a C toolchain are not needed.

| Platform | Archive | Minimum OS |
| --- | --- | --- |
| Linux x86-64 | `mekugi_linux_amd64.tar.gz` | glibc 2.39, such as Ubuntu 24.04 |
| Linux ARM64 | `mekugi_linux_arm64.tar.gz` | glibc 2.39, such as Ubuntu 24.04 |
| macOS Apple Silicon | `mekugi_darwin_arm64.tar.gz` | macOS 15 |

The [installer](install.sh) selects your platform, verifies the release checksum,
and installs both binaries into `~/.local/bin`, replacing existing copies:

```sh
curl -fsSL https://raw.githubusercontent.com/yusing/mekugi/main/install.sh | sh
```

Add `~/.local/bin` to your `PATH` if needed. Linux releases need glibc and do not
run on stock Alpine Linux; use a source build for other environments.

### Build from source

Source installs require **Go 1.27+**, CGO enabled, a C toolchain, and **Make**.
Build the coordinated workspace from a checkout:

```sh
git clone https://github.com/yusing/mekugi.git
cd mekugi
make install
```

Add `$GOBIN`, or `$(go env GOPATH)/bin` if that is unset, to your `PATH`.

`make install` installs both `mekugi` and `mekugi-exec`. `make uninstall`
removes only those binaries. Running sessions keep their worker executable, so
start a new session to pick up an update.

## Usage

### Start Mekugi

| Launch | Models | Authentication |
| --- | --- | --- |
| `mekugi` | Third-party `grok:...` and `opencode*:` IDs | [Grok](#grok-models) or [OpenCode](#opencode-go-and-zen) credentials |
| `mekugi grok` | Plain Grok IDs, such as `grok-4.7` | Grok credentials |
| `mekugi codex` | Codex models only | `codex login` with ChatGPT authentication |

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
model/effort/tier, layout, roster, child history, and retained Diff return; explicit
`-m`/`-c` overrides named settings. Drafts, selections, filters, and scroll positions do
not return. Third-party `exec resume`/`exec
fork` uses the mode's default model unless given `-m`. See [resume
details](doc/spec/native_ui.md).

### Approvals

Codex approvals open over an empty composer; otherwise press `Ctrl-B`, then `q`. Arrows
choose, Enter confirms, Esc hides without answering. Typing/pasting in Mekugi-mode
command approvals selects denial with an optional reason; feedback reaches the current
turn. Your draft/attachments return afterward.

The guard asks before supported Git/GitHub/Mercurial/SVN/Jujutsu remote writes in
interactive Mekugi mode, even with `--yolo`; headless/noninteractive runs have no guard.
It needs `mekugi-exec` beside `mekugi`, synchronous native command hooks, and unsandboxed execution.
Approve once, approve the exact command/workdir/executable for this UI session, or deny.
Denial, a five-minute timeout, or an unreachable approval connection fails only that
command with status 1; the shell follows normal `;`/`&&` behavior. Session grants expire
when the UI closes. Missing/conflicting trusted guard hooks block turns; explicit hook
overrides conflict. Read-only commands run without asking.

Coverage is incomplete for runtime-computed commands, `eval`, scripts/sourced files, and
arbitrary programs invoking VCS internally. See [guard
limits](doc/spec/execution.md#req-execution-003--guard-remote-vcs-writes).

Interactive sudo commands also ask for approval, even with cached credentials or
passwordless sudo. If authentication is needed afterward, a separate secret password
dialog keeps the answer out of model input and retained history. Sudo approval works
independently of `--vcs-guard` and grants no sandbox permissions. See
[sudo support and limits](doc/spec/execution.md#req-execution-004--unlock-sudo-through-the-native-dialog).

### Headless execution

```sh
mekugi headless --yolo < prompt.txt
```

Headless requires `--yolo` and one nonempty stdin prompt up to 16 MiB; model/`-c`
options are accepted, resume/positional prompts are not. It exits after the host turn
completes. Questions/approvals fail; Ctrl-C stops the run. Stdout
JSONL contains private host messages and a completion event;
diagnostics go to stderr. Run/shutdown failure exits nonzero. `codex exec` is unchanged.

## UI

Main starts at full width, with Agents below. Toggle Diff to split the view;
close it to restore full width. Click a child to switch the displayed transcript.
The composer always targets Main. Narrow terminals show one pane.

| Action | Shortcut |
| --- | --- |
| Show Main / toggle Diff / focus Agents | `Ctrl-B`, then `1` / `2` / `3` |
| Resize splits | Drag dividers, or `Ctrl-B`, then arrows; Up/Down in Agents adjusts its height |
| Resize Diff navigator | `Ctrl-B`, then `[` / `]` |
| Browse displayed transcript | `Ctrl-B`, then PageUp / PageDown |

The wheel scrolls under the pointer. Markdown supports colored code, responsive tables,
and terminal Mermaid flowcharts; unsupported diagrams remain source. `/title TITLE`
saves a manual title and skips automatic naming. Otherwise a new session may make a
separate GPT-6 Luna medium-reasoning title request; unavailable credentials or Luna
silently skip it. Notifications follow Codex's `tui.notifications`. See the [UI
contract](doc/spec/native_ui.md) for complete controls.

### Composer

- Enter steers; Tab queues. Type `?` in an empty draft for shortcuts.
- A breathing count shows running commands from Main and its children. `/ps` opens
  their live output, one tab per command, without interrupting work.
- `@` attaches files; `@!` includes ignored files. VCS metadata is excluded;
  unreadable/oversized content has omission notices. Directories attach two-level
  trees honoring `.gitignore`, bounded to 256 entries/16 KiB with truncation notices.
- Enabled `$skill-name` references attach instructions, using `skills-mgr` when available.
  Use it for managed skills, `/skills` for Codex toggles; click receipts for submitted contents.
- Select transcript or saved Diff text and press `r` to mention it.
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

Source-content edits stream, including patches, `cat` heredocs, and supported Python
writes. Copies, removals, formatters, and other command effects stay in saved changes.
Only completed edits become saved changes. Temporary views replace
only the editing agent's transcript. `/live` toggles them without stopping capture;
new launches enable them again. `v` switches views, `s` opens the navigator, `/` filters,
`n`/`p` changes files, `[`/`]` jumps hunks. Tab opens Changes by caller. Click an Edit
for its hunk; `Ctrl-B e` pins files, `Ctrl-B r` resumes following.
See [all diff controls](doc/spec/changes.md#live-terminal-view).

### Agents pane

Click a child to read its full transcript; click Main to return. Input stays with
Main. In Agents, arrows navigate threads and `a` returns to Main. In the transcript,
Esc resumes following; `Ctrl-B o` loads older child history. The roster shows active time, round trips,
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
is captured too. See [changes](doc/spec/changes.md) and [retained reads](modules/outputrecovery/SPEC.md).

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
| `--mode` | `mekugi` | `passthrough` forwards Codex without Mekugi tools |
| `--vcs-guard` | `true` | Ask before UI remote writes, even with `--yolo` |
| `--deduplicate-output` | `true` | Reference duplicates in model input; full results/evidence stay intact |
| `--grok-auth-file` | `~/.grok/auth.json` | OAuth store |
| `--timeout` | `10m` | Wait for response start |
| `--stream-idle-timeout` | `4m` | Limit provider-message or HTTP-byte gaps |
| `--capture-output PATH` | Disabled | Append sanitized JSONL metrics |
| `--debug` | Disabled | Record private [diagnostic evidence](docs/contributing/development.md#diagnostics) |

`mekugi --mode passthrough --vcs-guard=false codex` retains Codex policy/metrics, but
disables Mekugi tools/guard. Use `--ansi-faint=off` if a multiplexer hides mosh.

### Context reset and recovery

Manual `/compact` and context-full compaction use Codex and the provider. Mekugi
adds no recovery-injection hook. Captured changes and retained output remain
readable while their [storage](#replay-storage) is retained.

### Troubleshooting and integrations

- **Instructions:** wrapped sessions disable `/goal`; `<!-- mekugi:omit -->` through
  `<!-- /mekugi:omit -->` omits marked blocks without changing files/unmarked policy.
  With `skills-mgr`, managed selections attach instructions and the stock catalog is
  disabled for the session. See [guidance](modules/agentguidance/SPEC.md).
- **Recovery:** see [resets](#context-reset-and-recovery) and [storage](#replay-storage).
- **Frontend commands:** [compiled helpers](doc/spec/plugin.md) run through stock
  execution. JavaScript plugin loading has been removed; existing plugin files
  are left untouched.
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
