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
- **Isolated parallel work.** [Orchestration](#orchestration) coordinates fresh
  Codex threads in separate checkouts, then Main reviews and integrates their results.
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
- For configured JavaScript plugins only: **Node.js 24+** as `node`. Plugins declaring regex grammars also require **ripgrep** as `rg` on the router's `PATH`.
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

Source installs require **Go 1.27+**, CGO enabled, and a C toolchain:

```sh
go install github.com/yusing/mekugi/cmd/mekugi@latest github.com/yusing/mekugi/cmd/mekugi-exec@latest
```

Add `$GOBIN`, or `$(go env GOPATH)/bin` if that is unset, to your `PATH`.

From a checkout with **Make**, run `make install`. It regenerates the
optional plugin shared core and installs `mekugi` and `mekugi-exec`. `make uninstall`
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
model/effort/tier, layout, roster, Activity, and retained Diff return; explicit
`-m`/`-c` overrides named settings. Drafts, selections, filters, and scroll positions do
not return. UI histories above 16 MiB cannot resume. Third-party `exec resume`/`exec
fork` uses the mode's default model unless given `-m`. See [resume
details](doc/spec/native_ui.md).

### Orchestration

Use `/orchestrate ISSUES` in Main's composer to coordinate parallel batches in
isolated checkouts. Supported sources are Git, SVN and unversioned directories.
Git is required; SVN sources also need the SVN client. Git and SVN batches start
from locally recorded committed baselines, excluding local edits; unversioned
batches snapshot the current tree with ignore rules. Main prepares required ignored
inputs before starting each fresh child thread.

Bare `/orchestrate` opens this Main's batch picker. Enter on an Orchestration row
in Agents switches threads; `Ctrl-B`, then `[` / `]` cycles them. Each thread keeps
its draft and panes while background threads continue working. Input targets the
viewed thread; labeled questions and approvals return answers to their source.

Main reviews and integrates Git batches with native Git, or SVN/unversioned batches
with shadow writeback, then accepts their journal tasks. Explicit cleanup removes
accepted, idle, clean run-owned checkouts and unchanged copied evidence. Branches,
manifests, journals and shadow history remain; unfinished or unintegrated work stays.
`/quit` asks for confirmation while batches run. Resume restores the roster;
interrupted turns stay interrupted. See the [orchestration contract](doc/spec/orchestrate.md)
for preparation, integration, cleanup and recovery details.

### Approvals

Codex approvals open over an empty composer; otherwise press `Ctrl-B`, then `q`. Arrows
choose, Enter confirms, Esc hides without answering. Typing/pasting in Mekugi-mode
command approvals selects denial with an optional reason; feedback reaches the current
turn. Your draft/attachments return afterward.

The guard asks before supported Git/GitHub/Mercurial/SVN/Jujutsu remote writes in
interactive Mekugi mode, even with `--yolo`; headless/noninteractive runs have no guard.
It needs `mekugi-exec` beside `mekugi`, native MCP hooks, and unsandboxed execution.
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
| Resize Diff navigator without orchestration; otherwise cycle threads | `Ctrl-B`, then `[` / `]` |
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
