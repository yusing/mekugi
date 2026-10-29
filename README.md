# mekugi

A mekugi is the small peg that pins a Japanese sword's handle to the blade.
Take it out and the handle comes off. Leave it in and the blade is still the
blade.

Mekugi adds compact, recoverable tools and live subagent activity to stock
Codex. Codex keeps editing, execution, the sandbox, permissions, command
sessions, and patch review. Invocation overrides leave Codex configuration unchanged.

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
  to earlier visible text by default. Full host results, retained evidence,
  and the UI stay intact. Transcript suffixes mark repeated output; `/session`
  shows occurrences and estimated saved tokens. Disable with
  `--deduplicate-output=false`;
  see the [projection contract](modules/outputdeduplication/SPEC.md).
- **Session continuity.** Retained change evidence and output remain available across
  resume and forks, subject to [storage retention](#replay-storage).
- **Coordinated delegates.** [Start fresh agents with selected files and skills](modules/threadcontrol/SPEC.md#prepared-children-and-guidance)
  already loaded, inspect their context and applied settings, and wait for one or
  all selected agents without polling.
- **Context checkpoints.** Agents can [compact at safe checkpoints](modules/threadcontrol/SPEC.md#operations-and-admission)
  and continue unfinished work, or compact before waiting for delegates.
  [Selected files and skills reload](modules/agentguidance/SPEC.md#compaction-context)
  from source after Codex compaction.
- **Context usage notices.** Agents receive [measured context reminders](modules/agentguidance/SPEC.md#context-usage-notices)
  as their window fills, starting at halfway, when native measurements are available.
- **Workspace facts up front.** Codex agents receive an [observed OS and Git snapshot](modules/agentguidance/SPEC.md#environment-snapshot),
  including branch, status and recent commits, without a separate discovery command.
- **Claude delegates.** [Select a Claude model](#claude-delegates) through the same
  agent coordination tools, with shared captured-change reads and parent delivery.

### User-facing

- **Native terminal UI.** [Main, Diff, and Agents](#ui)
  share one terminal without an external pane manager.
- **Server-owned threads.** [Create and control threads on an execution machine](#server-and-remote-control)
  through a private local socket or an authenticated connector. Continue the same
  thread from a native terminal or remote client; closing either leaves work running.
- **Browser control across machines.** [Connect from a desktop or phone](#browser-ui)
  to create, follow and control threads, or replay saved sessions. Each connection
  keeps its own machine identity and reconnects after page reload. Install it as
  a standalone app through a supporting browser.
- **Live subagent activity.** See model, effort, progress, and child-authored reply summaries
  in Main or the [Agents pane](#agents-pane), alongside elapsed time and edit activity.
  Open attachable children to read their transcripts, send input and adjust their settings.
- **Conversation branches.** [Resume saved conversations, fork branches, or rewind](#resume)
  to an earlier user turn with optional captured-edit reversal.
- **Live diffs.** [Inspect streaming previews and saved edits](#live-diff-pane);
  click an Edit event to open its captured file and hunk.
- **Readable command output.** Open [searchable retained output](#output-dialog).
  With `mekugi-exec`, supported Bash and Linux dash-backed sh command lists show
  each command's streamed output, exit status, and timing.
  Markdown file links open local source in the same dialog.
  Supported single commands and pipelines, including Bash timed commands, show measured
  command time excluding startup and matching.
- **Individual process control.** Follow live commands from Main and its children in
  [`/ps`](#composer) or the [browser's process controls](#browser-ui), and stop a
  selected command when the host exposes its process identity.
- **Image previews.** Viewed and generated images appear in the transcript on
  Kitty- or Sixel-capable terminals. Expand activity with Ctrl-E to show viewed
  images; generated images accompany their saved paths. Unsupported terminals,
  unavailable files and terminal multiplexers retain readable text entries.
  The [browser UI](#browser-ui) also previews attached, viewed and generated images.
- **File, directory and skill attachments.** [Send selected contents or a bounded directory tree directly](#composer)
  without a separate agent read; unreadable or oversized attachments have explicit notices.
- **Composer additions.** Mention selected messages or diffs, ask side questions,
  or run [one-off tasks](#composer) without interrupting Main. One-off edits still
  affect the shared workspace.
- **Reusable presets.** Save ten numbered prompts or slash commands and recall
  them from the shared terminal and browser [composer](#composer).
- **Editable next-message suggestions.** [Accept a suggested follow-up for editing](#composer)
  when supported OpenAI accounts, models and remote configuration make predictions available.
- **Usage, throughput and cost.** See per-thread usage and estimated API costs
  in Agents, with output tokens/sec in Main's composer and child-agent rows,
  plus launch-wide average throughput, usage
  coverage, retries, and diagnostics in [`/session`](#metrics).
- **Other providers and tiers.** Use [Grok](#grok-models) or
  [OpenCode Go and Zen](#opencode-go-and-zen), and choose [service tiers](#mekugi-settings).
- **In-session settings and approvals.** Change [model, reasoning, tier and permission settings](#composer)
  without rewriting configuration. Optional [remote-write and sudo guards](#approvals)
  ask before supported sensitive commands, even with `--yolo`.
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
- Claude delegates additionally need Bun and an authenticated Claude Code
  installation on the execution machine's `PATH`.

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

Source installs require **Go 1.27+**, CGO enabled, a C toolchain, **Make**,
**Bun 1.4.4+**, and **Node.js 24.21+** for browser assets.
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
disable Codex approvals/sandboxing while keeping the guard. To disable Mekugi's
remote-write and sudo guarding while retaining Codex policy, use `--shell-guard=false`.

```sh
mekugi codex
mekugi grok -m grok-4.7
mekugi -m opencode-go:glm-5.3
mekugi exec "Explain this repository"
```

Interactive launches start the local server when absent and attach the [UI](#ui).
Closing the UI leaves its thread running; exit prints resume/replay commands.
Ctrl-C cancels the startup wait. Noninteractive commands retain Codex's
arguments/output/exit status.
Mekugi needs JavaScript `exec`. Custom inference endpoints and `--oss` are unsupported.
See [launch limits](apps/mekugi/internal/launch/SPEC.md) and
[request integration](docs/contracts/responses-transport.md); invocation overrides leave config unchanged.

### Server and remote control

Native launches start the local server automatically. To keep a machine available
for remote clients without opening a terminal UI, start it explicitly:

```sh
mekugi server
```

It runs in the foreground and prints its private Unix socket path. By default,
the socket is under the user cache at `mekugi/server/control.sock`; `--socket PATH`
selects another private location. Local clients use the same-user socket and the
versioned [Session API](apps/mekugi/internal/sessionapi/CONTRACT.md). Workspaces,
models and host attachment paths belong to this execution machine.

For remote clients, run a connector on that same machine:

```sh
mekugi connector --origin https://ui.example.com
```

The connector listens on `127.0.0.1:7778` and prints its token-file location,
without printing the token. Its default token file is `connector/token` beside
the server socket. Read it privately to configure the client's bearer credential;
keep it out of URLs and logs. `--token-file PATH` selects a different private file,
`--socket PATH` selects the server, and repeated `--origin` flags admit additional
UI origins. Origin permission never replaces token authentication.

Terminate WSS at a reverse proxy and keep its connection to the connector
on loopback or a protected private network. Forward WebSocket upgrades and
allow the API's
[attachment request budget](apps/mekugi/internal/sessionapi/CONTRACT.md#attachment-input).
Browser clients connect directly to each connector; the UI host receives neither
machine credentials nor control requests.

Clients authenticate in the first control message; credentials stay out of URLs
and upgrade headers. Send SIGHUP to the connector process to rotate its token.
Rotation closes connections using the old credential; clients need the new token. Closing the
connector or a client leaves server-owned work running. Stopping the server ends
its active work; saved threads remain resumable. Restart never automatically
replays requests or restarts work. See the [connector contract](apps/mekugi/internal/connector/SPEC.md)
for admission and deployment details.

### Browser UI

Run the assets host on any machine:

```sh
mekugi web --listen 127.0.0.1:7777
```

Open that address and connect the execution machine with a name, its connector
URL and its access token; later machines are added under **Machines** at the foot
of the sidebar. There is no default connection.
Configure the connector's allowed origin to match the page's exact origin, for
example `mekugi connector --origin http://127.0.0.1:7777` for local use. For remote
use, serve the page over HTTPS and the connector through a WSS reverse proxy.
Machines and access tokens are saved in this browser for the page's origin, so
reload reconnects them and restores the thread selected in the page address. Use
a trusted browser. Removing a machine under **Machines** deletes its saved token;
clearing this site's browser data forgets all machines. Browser storage failures
show a warning while connections remain usable for the current page.

Install Mekugi using your browser's **Install app**, **Add to Dock** or
**Add to Home Screen** action, where supported. Installation requires HTTPS or
a localhost/loopback address. The installed app still needs the UI host and its
execution-machine connectors online.

The sidebar lists each machine's threads. **New thread** starts one from its
first message; the composer's **+** menu also resumes a saved thread or opens a
replay. Workspaces, models and attachment paths refer to the chosen machine.
Upload files from the browser or attach host paths in order, up to 32 MiB of
decoded uploads per message. As in the terminal, `/`, `@` and `$` complete
commands, files and skills, and a leading `!` runs shell input; Enter sends or
steers, and Tab queues while Main works. Answer secret prompts in the native
terminal.

Active processes stay above the composer, with **Stop** for each command whose
host exposes individual process control. Other commands remain visible with a
disabled Stop and an explanation. `/session` opens the shared session metrics;
`/status` shows grouped settings and usage. Web links open in a new tab, while
Markdown file links open details from a connected live thread on the selected
execution machine.

Submissions rejected after admission offer **Retry intact** or **Discard**. Retry reuses the original
attachment snapshots. Resolve unsent input before leaving the thread or reconnecting;
its recovery belongs to that control view. A refused first message leaves the opened
thread available with its draft.

Closing the page or disconnecting a machine leaves its work running. **End thread**,
in the thread's session details, explicitly stops that thread. On its execution machine, `mekugi resume THREAD_ID`
continues it from a native terminal. Replay is read-only, shares playback controls
between viewers, and requires the live thread to be ended first. Completed playback
stays open until ended. If a request loses its acknowledgement, check that machine's
state before resubmitting; the browser does not retry mutations automatically.

Source checkouts need `make webui` before embedding browser assets. See
[development checks](docs/contributing/development.md#build-and-test) and the
[browser specification](apps/mekugi/internal/webui/SPEC.md).

#### Docker deployment

The root `Dockerfile` builds browser assets with Bun and embeds them in a CGO-enabled
Go binary. `compose.yaml` runs separate web UI and connector containers. The
execution server, Codex, credentials and workspaces stay on the execution machine.

For a Docker Engine on that machine with Tailscale connected, start `mekugi server`, then:

```sh
cp .env.example .env
# Edit .env: set MEKUGI_SERVER_DIR to the printed socket's parent directory,
# and MEKUGI_UID / MEKUGI_GID to the server user's numeric id -u / id -g.
# Set TAILNET_IP from tailscale ip -4 and WEBUI_ORIGIN to http://TAILNET_IP:7777.
install -d -m 700 .docker/connector
docker compose up --build -d
```

The socket directory must be private (0700). The connector must run with the
same host UID as the server; container user-namespace mappings must preserve that
identity. Host Unix-socket sharing requires a compatible Docker host; Docker
Desktop's VM cannot generally use a native macOS/Windows server socket.
The directory mount follows socket replacement when the server restarts.

From a permitted tailnet device, open `http://TAILNET_IP:7777` and add
`http://TAILNET_IP:7778` as a machine, substituting the configured IP.
Read `.docker/connector/token` privately for the access token. It is generated at
first startup and persists across container replacement. Keep `.env` and
`.docker/` private and out of version control. If changing `CONNECTOR_STATE_DIR`,
create that directory with the same ownership and permissions first.

Published ports bind only to the configured Tailscale IPv4 address. Tailnet access
rules must permit the browser device to reach both ports. Tailscale encrypts this
traffic; the listeners themselves use HTTP and WebSocket. For HTTPS/WSS, terminate
TLS at a reverse proxy, set `WEBUI_ORIGIN` to the exact page origin and use its
connector URL. Forward WebSocket upgrades and the attachment budget described
above. Changing the UI address or port also requires updating `WEBUI_ORIGIN`.
Rotate credentials with `docker compose kill -s SIGHUP connector`; enter the new
token in the browser. `docker compose down` stops these two services and retains
the token directory and server-owned work.

### Grok models

Use `grok login --oauth` or `XAI_API_KEY` (takes precedence); Codex credentials stay separate.
The default follows `[models].default` in `~/.grok/config.toml`, then the latest standard model (currently `grok-4.7`);
`-m`/`-c model=...` overrides it. Standalone IDs need `grok:`; `mekugi grok` uses plain IDs.
Subagents inherit full history by default; `fork_turns="none"` starts fresh. Build Fast needs OAuth;
encrypted OpenAI history cannot switch to Grok. See [Grok requirements](providers/grok/SPEC.md).

### OpenCode Go and Zen

```sh
OPENCODE_GO_API_KEY='your-key' mekugi -m opencode-go:glm-5.3
OPENCODE_ZEN_API_KEY='your-key' mekugi -m opencode-zen:kimi-k3
```

Keys can also live in [settings](#mekugi-settings). `OPENCODE_API_KEY` overrides file
keys; per-service variables override it, and an empty value disables that service.
Models/prices refresh hourly; the picker updates next launch. See
[OpenCode](providers/opencode/SPEC.md).

### Claude delegates

Ask the Codex parent to call `threadcontrol.list_models` with no arguments,
then use a returned ID with `spawn_agent` and `fork_turns:"none"`. Discovery reads
Claude's native advertised catalog without submitting model input; account access
is still validated during execution. Roles, messages, follow-ups, interruption
and waits use Thread Control.
Explicit `mchanges` IDs and ranges are shared with the parent; implicit selections
remain scoped to the author.

Claude delegates use approval handling without filesystem or network sandboxing.
Their approval requests appear in the ordinary parent dock. Roster rows show
available facts and cannot be opened as independent interactive sessions.
Ordinary Codex execution does not require the optional Claude runtime.
Source builds need `make claude-worker` before rebuilding; release and installation
recipes bundle it automatically. See the [Claude backend](backends/claude/SPEC.md).

### Resume

`resume THREAD_ID` attaches to that thread if it is still running on the local
server, or restores its saved session otherwise. `resume --last` selects the latest
saved session; bare `resume` opens the
workspace picker. Type to search, Tab for all workspaces, Enter to resume. Inside an
idle session, `/resume` opens the picker or `/resume THREAD_ID` switches directly. Saved
model/effort/tier, layout, roster, child history, and retained Diff return; explicit
`-m`/`-c` overrides named settings. Drafts, selections, filters, and scroll positions do
not return. Third-party `exec resume`/`exec
fork` uses the mode's default model unless given `-m`. See [resume
details](apps/mekugi/internal/sessionpresentation/SPEC.md#resume-and-switching).

While idle, `/fork` takes no arguments and creates a saved branch of the completed
conversation, then switches to it with current settings and pending input. The
original stays resumable and workspace files remain unchanged. A failed fork
keeps the original session usable.

`/rewind` opens a user-turn dialog while idle, after the first user turn. Select a turn to branch immediately
before it, excluding that turn and later conversation while keeping current
settings and pending input. Choose Keep edits (the default) or Revert captured
edits from that turn onward, including descendant agents. When there are no such
captured edits, rewind skips this choice. Revert uses `mchanges`
and preserves unrelated edits through its normal merge behavior. Missing evidence
disables Revert; conflicts stop that rewind. Keep remains available. Retained
change/output evidence stays available, including any recorded reversal.
After switching, a separate confirmation offers to archive the original thread;
No is selected by default. Declining keeps both sessions. Archive failure leaves
the new branch usable and the original available.

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
when the UI closes. At startup, incompatible caller hooks produce a warning and
are disabled for that invocation. Other hooks and configuration files remain
unchanged. Managed conflicts, unverifiable matchers, or missing/conflicting guard
hooks at turn admission block with an explanation. Read-only commands run without asking.

Coverage is incomplete for runtime-computed commands, `eval`, scripts/sourced files, and
arbitrary programs invoking VCS internally. See [guard
limits](modules/remotewriteapproval/SPEC.md).

Interactive sudo commands also ask for approval, even with cached credentials or
passwordless sudo. If authentication is needed afterward, a separate secret password
dialog keeps the answer out of model input and retained history. The shared
`--shell-guard` toggle controls both sudo and remote-write approval and grants no
sandbox permissions. See
[sudo support and limits](modules/sudoapproval/SPEC.md).

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
Live subagents use their own composer, prompts and settings. Main, children and
Side retain separate drafts; historical views remain read-only. Narrow terminals show one pane.

| Action | Shortcut |
| --- | --- |
| Show Main / toggle Diff / focus Agents | `Ctrl-B`, then `1` / `2` / `3` |
| Resize Main/Diff split | Drag its divider, or `Ctrl-B`, then Left/Right; repeat arrows within 500 ms without another prefix. Agents height fits its content |
| Resize Diff navigator | `Ctrl-B`, then `[` / `]` |
| Browse displayed transcript | `Ctrl-B`, then PageUp / PageDown |

The wheel scrolls under the pointer. Markdown supports colored code, responsive tables,
and terminal Mermaid flowcharts; unsupported diagrams remain source. `/title TITLE`
saves a manual title and skips automatic naming. Otherwise a new session may make a
separate GPT-6 Luna medium-reasoning title request; unavailable credentials or Luna
silently skip it. Notifications follow Codex's `tui.notifications`. See
[terminal controls](apps/mekugi/internal/terminalsession/SPEC.md) and
[session behavior](apps/mekugi/internal/sessionpresentation/SPEC.md).

### Composer

- Enter steers; Tab queues. Type `?` in an empty draft for shortcuts.
- `/predictions on|off` controls next-message suggestions in the empty Main composer.
  Tab accepts a suggestion for editing; typing dismisses it. Availability depends
  on the OpenAI account, model and remote configuration, and requires Codex
  0.162.0-alpha.20 or newer. Third-party providers are ineligible.
- `/presets` (or Ctrl+B then P) opens slots 1–9 and 0. Recall fills the composer without sending;
  the terminal keeps the previous draft in input history. New and Edit use `$EDITOR`
  in the terminal, like Ctrl+G, then ask for an optional label; Rename changes only
  the label. Without a label, the menu shows the input's first line. New, Edit,
  Rename and Delete manage slots, with confirmation before replacement or deletion.
  Slots use Codex's native `tui.presets` on the execution machine, including its
  active user/profile configuration and labels.
  Existing Mekugi preset files remain untouched and unused. Presets contain text,
  not attachments.
- A breathing count shows running commands from Main and its children. `/ps` opens
  their live output, one tab per command. Select a process with Left/Right and
  press `s` to stop it when the host provides its process identity. Viewing or
  closing the dialog leaves work running; stopping closes it and reports failures.
- `@` attaches files; `@!` includes ignored files. VCS metadata is excluded;
  unreadable/oversized content has omission notices. Directories attach two-level
  trees honoring `.gitignore`, bounded to 256 entries/16 KiB with truncation notices.
- Enabled `$skill-name` references attach instructions, using `skills-mgr` when available.
  Use it for managed skills, `/skills` for Codex toggles; click receipts for submitted contents.
- Select transcript or saved Diff text and press `r` to mention it.
- `/side` creates a fresh answer-only snapshot with a Side tab beside Main. Only one
  Side is open; repeat `/side` to choose Replace or Cancel. Select Side to read or
  reply with its own draft.
  Esc closes it and interrupts only its answer. Side, One-off and BTW retain native
  history in internal threads, hidden from normal session selection and archived
  when their operations close. Closed slots are not restored on resume. If
  compaction is needed, compact Main and create a fresh side.
- `/oneoff TASK` runs isolated work in the same side slot, inheriting Main's tools
  and permissions. It selects One-off immediately and keeps the completed result
  available. Main stays usable and receives no one-off transcript; file edits and
  other external effects still affect the shared workspace. Replace or Cancel
  chooses what happens when Side or One-off is already open.
- `/btw QUESTION` opens an independent temporary answer dock on Main. It stays
  alive but hidden while viewing Side/One-off. Return to Main for `/side`,
  `/oneoff`, `/fork` and `/btw`; repeat `/btw` for a follow-up. Esc on Main
  closes only that dock.
- `/model`, `/effort` (or `/reasoning`), and `/tier` open pickers or accept values.
  Combine choices with `/model MODEL --reasoning=high --speed=fast`; omit MODEL
  to keep it, and use `--speed=standard` to clear the tier.
  `/tier default` clears the tier. Changes reach upcoming steps without editing config.
  Model and reasoning controls, including Shift+Up/Down, target the selected Main,
  Side or One-off conversation independently. Service-tier controls target Main.
- `/permissions` lists Main's permission profiles. Select one, or use
  `/permissions PROFILE --approval-policy=on-request --reviewer=user`.
  Changes apply from the next turn, after Codex confirms them. Queued input waits
  while choosing; Esc cancels. Running commands and existing approvals are unchanged.
- `/compact` resets context: Enter interrupts then resets, Tab queues. `/clear` starts
  fresh while idle, preserving sessions/files. See [reset policy](#context-reset-and-recovery).
  `/btw` and `/compact` requests use low reasoning when the model advertises it,
  preserving already-lower efforts and conversation settings.
- `/lock` prevents Esc/Ctrl-C interruption and Ctrl-C exit; `/unlock` restores them.
  The lock survives resume; explicit commands and steering still work.

Alt+Up or Shift+Left restores locally waiting input. Esc interrupts then resends only
uncommitted steers; committed input is never resent and Tab-queued input stays queued.
Without steers, Esc interrupts without resending. Ctrl-C clears the draft first, then
interrupts/restores input. Picker/help/selection dismissal takes precedence. Copying
needs OSC 52; Linux image paste needs `wl-paste` or `xclip`. See [input
details](apps/mekugi/internal/terminalsession/SPEC.md#composer-controls).

### Live diff pane

Source-content edits stream, including patches, `cat` heredocs, and supported Python
writes. Copies, removals, formatters, and other command effects stay in saved changes.
Only completed edits become saved changes. Main shows live edits from its session
family with source attribution; a selected child shows only its own edits.
`/live` toggles them without stopping capture;
new launches enable them again. In saved Diff, `s` opens the navigator, `/` filters,
`n`/`p` changes files, `[`/`]` jumps hunks. Tab opens Changes by caller. Click an Edit
for its hunk; `Ctrl-B e` pins files, `Ctrl-B r` resumes following.
See [saved-review controls](modules/changehistory/SPEC.md#terminal-review) and
[live-view controls](modules/livepreview/SPEC.md#terminal-live-views).

### Agents pane

Click an attachable child to read its full transcript; click Main to return.
Claude delegate rows remain noninteractive and show only available metrics.
Each runtime keeps its own composer draft, prompts and settings. The child composer
shows its role, model, reasoning effort and service tier. Historical views remain
read-only. The Side tab selects a separate editable conversation.
In Agents, arrows navigate threads and `a` returns to Main. In the transcript,
Esc resumes following. The roster shows active time, round trips,
throughput, usage, estimated API cost, and composed captured edits. Usage/rates survive
resume while retained; `≥` marks lower bounds, `?` uncertain edits, unavailable metrics stay blank.
Main's throughput appears beside composer context. Herdr is optional; redirected
sessions keep Codex input/output. See [activity details](modules/activity/CONTRACT.md).

### Output dialog

Click output/errors/excerpts for retained content; `Ctrl-B !` opens the newest error.
Supported Bash/dash-backed sh lists show per-command output/status/time; missing boundaries
are labeled combined. Host-omitted text cannot be recovered. Left/Right switches tabs;
`/` searches, `n`/`N` moves matches, `y` copies, Esc/`q` closes. Select text to copy portions.
Local links read current UTF-8 files up to 8 MiB; relative paths use the workspace,
`:line` scrolls, reopening rereads. See [dialog controls](modules/activity/CONTRACT.md)
and [command tracking](modules/commandsegments/SPEC.md).

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
provisional; missing evidence is labeled. Ignored untracked files require explicit named
targets to be retained and reverted/reapplied. Revert merges later edits, may create conflicts, and
is captured too. See [changes](modules/changehistory/SPEC.md), [capture coverage](docs/contracts/change-evidence.md), and [retained reads](modules/outputrecovery/SPEC.md).

## Configuration

### Mekugi settings

Create `mekugi/config.toml` under `$XDG_CONFIG_HOME` (default `~/.config` on Linux,
`~/Library/Application Support` on macOS). Optional sections are read at startup, never
rewritten:

```toml
[features]
shell_guard = true
deduplicate_output = true
child_reply_summary = true
debug = false

[service_tiers]
"gpt-6-astra" = "fast"
[providers.opencode_go]
api_key = "your-go-key"
[providers.opencode_zen]
api_key = "your-zen-key"
```

Feature keys set defaults for the matching hyphenated CLI flags in every launch,
including `mekugi codex`, independently of provider credentials. Each feature uses
an explicit CLI value first, then its file value, then the built-in default shown
above. Missing files, sections, and keys keep built-in defaults. Unknown keys and
non-boolean feature values fail startup without printing configuration contents.

For example, set `deduplicate_output = false` to keep repeated model input by
default, then run `mekugi --deduplicate-output codex` to enable it for one launch.
Bare boolean flags mean true; use `--debug=false` or `--shell-guard=false` to
explicitly disable a feature. Repeated flags use the last value. Flags must precede
`codex` and `--`; arguments after the delimiter belong to Codex. Overrides apply
to that invocation and its child threads, never write the file or saved session
settings, and are preserved in the printed resume command. A fresh launch or
resume reads the current file again.

Child replies normally include a short authored Summary and supporting Details in
one response. Main shows the complete summary; **Open reply** shows supporting
details without repeating it. Full-copy actions keep the original response.
Plain or malformed replies keep the excerpt fallback. Set
`child_reply_summary = false` or launch `mekugi --child-reply-summary=false codex`
to use excerpts for all replies and omit the child-authoring rule. This adds no
separate summarization request and leaves parent input and child transcripts intact.

Exact model IDs select tiers: `auto`, `default`, `fast` (sent as `priority`),
`priority`, or `flex`, subject to provider support. `/tier` choices override defaults
for that thread/model during the invocation. The composer shows the effective tier;
`/session` shows the provider-returned tier.

### Options

| Flag | Default | Purpose |
| --- | --- | --- |
| `--ansi-faint` | `auto` | Detect mosh; `on` uses ANSI faint, `off` fixed muted colors |
| `--shell-guard` | `true` | Guard UI remote writes and sudo commands, even with `--yolo` |
| `--deduplicate-output` | `true` | Reference duplicates in model input; full results/evidence stay intact |
| `--child-reply-summary` | `true` | Show child-authored summaries in Main, with the original behind Open reply |
| `--grok-auth-file` | `~/.grok/auth.json` | OAuth store |
| `--timeout` | `10m` | Wait for response start |
| `--stream-idle-timeout` | `4m` | Limit provider-message or HTTP-byte gaps |
| `--capture-output PATH` | Disabled | Append sanitized JSONL metrics |
| `--debug` | `false` | Record private [diagnostic evidence](docs/contributing/development.md#diagnostics) |

The boolean defaults above apply when no matching [feature setting](#mekugi-settings) exists.
Use `--ansi-faint=off` if a multiplexer hides mosh.

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
- **Frontend commands:** [compiled helpers](apps/mekugi/internal/frontends/SPEC.md) run through stock
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
inspect-storage`; see [storage details](modules/sessionhistory/SPEC.md).

## Metrics

`/session` shows launch-wide usage/retries/coverage/throughput/cache/transport.
Left/Right or Tab switches tabs; Up/Down/PgUp/PgDn scrolls, `[`/`]` selects exchanges,
`r` refreshes, Esc closes. Launch totals do not restore; the roster retains thread
usage. Throughput divides authoritative output tokens (including reasoning) by measured
request seconds, including latency/retries, excluding tools/idle time. Missing
measurements are unavailable; costs are API estimates, not subscription charges. Local
estimates are not billing figures. See [metrics](modules/transportdiagnostics/SPEC.md).

```sh
curl -sS "${MEKUGI_BASE_URL%/v1}/api/metrics"
mekugi --capture-output capture.jsonl codex
```

Captures exclude prompts, patches, and credentials.

## Documentation

[Documentation](docs/index.md) · [Contributing](CONTRIBUTING.md) ·
[Comparisons](docs/benchmarks.md) · [Codex
end-to-end checks](docs/codex-router-e2e.md)

## License

MIT. See [LICENSE](LICENSE).
