# mekugi

A *mekugi* is the small peg that pins a Japanese sword's handle to the blade.
Take it out and the handle comes off. Leave it in and the blade is still the
blade.

**Mekugi is a fast, source-grounded workspace for Codex.** It keeps long
sessions and multi-agent workflows lean, observable, and surgically reversible,
while leaving Codex's editing, execution, sandbox, permissions, and config
untouched.

[Why Mekugi](#why-mekugi) · [Install](#install) · [Quick start](#quick-start) ·
[Guides](#guides) · [Documentation](#documentation)

## Why Mekugi

### Zero-summary compaction & compact context

- **Compaction that rereads real sources.** Reset context without lossy LLM
  handover summaries. Agents select the exact files, skills, and captured
  changes needed for unfinished work, and Mekugi reloads them fresh before the
  next turn.
- **Agent-chosen checkpoints & usage notices.** Measured context-usage notices
  start at 50% and tell the agent to compact at 90%, so agents reset cleanly at
  safe checkpoints instead of drifting inside a bloated window.
- **Built-in token-saving helpers.** Wrapped sessions put `mcat` (batched UTF-8
  reads), `mrun` (head/tail-bounded commands with read-only spill files), `msymbol`
  (Go, TypeScript, Python, and Rust semantic definitions/references), and `mfinspect`
  (structural outlines) on `PATH`. Automatic output deduplication replaces repeated
  outputs with compact references while keeping full results in history and the UI.
- **Workspace facts up front.** Sessions start with an observed OS and Git
  snapshot and mark attached files and skills as reusable, cutting warm-up turns.

### Multi-agent workflows without the warm-up tax

- **Preloaded delegates.** Fresh subagents start with selected files, skills,
  and captured changes already loaded under their own permissions, with no wasted
  first turns rediscovering parent context.
- **Non-polling waits with progress check-ins.** Parents wait for any or all
  descendants without polling loops. Optional progress yields pair brief
  developer status summaries with exact captured changes so coordinators catch
  wrong turns early.
- **Side questions & isolated one-offs.** Ask quick read-only questions with
  `/btw` or spin up an isolated work-capable `/oneoff` branch without polluting
  Main's transcript, and coordinate across active sessions.

### Exact change tracking & surgical undo

- **Every edit gets a short ID.** Observed patches and shell writes are
  captured as durable change IDs (`mchanges`). Hand exact ranges (like
  `amber1..amber3`) to reviewers or across compaction instead of dumping a noisy
  worktree diff mixed with your own edits.
- **Revert only what you reject.** Undo specific captured edits (optionally
  scoped to individual paths) with three-way merging that preserves your
  uncommitted edits and other agents' work. Every revert is captured too and
  can be reapplied anytime. `/rewind` can roll back conversation turns and
  their captured edits together.

### Live visibility, control & safety guards

- **Watch edits stream live.** Code changes stream into the
  [Live Diff pane](docs/guide/terminal-ui.md#live-diff-pane) as the agent
  writes them. Operations appear under the reasoning that led to them, and the
  [Agents sheet](docs/guide/terminal-ui.md#agents) lets you inspect subagent
  transcripts, metrics, and steer them live.
- **Intervene before mistakes land.** Steer the running turn (`Enter`), queue
  the next (`Tab`), interrupt (`Esc`), or inspect and stop one running command
  without killing the turn (`/ps`).
- **Remote-write & `sudo` guards, even with `--yolo`.** Interactive sessions
  prompt before supported Git, GitHub, Mercurial, SVN, and Jujutsu remote
  writes and `sudo` commands (requires unsandboxed execution; see
  [Approvals and guards](docs/guide/approvals.md)).
- **Private secrets, never in model context.** Enter `sudo` passwords and GPG
  passphrases (including Git signing) in a dedicated masked dialog that stays
  completely out of model input and session history. New or modified Codex
  hooks ask for trust before work starts.

### Terminal & browser UI, server-owned sessions

- **Detach without killing work.** Sessions are owned by a local background
  server: close your terminal or browser and agents keep running. Reattach
  anytime from the terminal or control multiple machines from desktop or phone
  through the [Web UI and authenticated connector](docs/guide/remote.md).
- **Full session lifecycle.** [Resume, fork, or rewind](docs/guide/sessions.md)
  sessions, inspect per-agent usage, throughput, and cost (`/session`), replay
  past sessions offline, use [reusable prompt presets](docs/guide/terminal-ui.md#composer),
  and connect [custom OpenAI-compatible Responses providers](docs/guide/providers.md).

## Install

Requirements:

- The **managed native runtime**, bundled in release archives and source builds.
  Use **Codex CLI** directly for `codex login`.
- Built-in helpers need neither Node.js nor Bun. Semantic lookup (`msymbol`)
  uses language servers on your `PATH`; see
  [Agent tools](docs/guide/agent-tools.md#wrapped-session-helpers).

### Release binaries

| Platform | Archive | Minimum OS |
| --- | --- | --- |
| Linux x86-64 | `mekugi_linux_amd64.tar.gz` | glibc 2.39 (e.g. Ubuntu 24.04) |
| Linux ARM64 | `mekugi_linux_arm64.tar.gz` | glibc 2.39 (e.g. Ubuntu 24.04) |
| macOS Apple Silicon | `mekugi_darwin_arm64.tar.gz` | macOS 15 |

Install `mekugi`, `mekugi-exec`, the native server, helpers, and model catalog
into `~/.local/bin` (checksum-verified):

```sh
curl -fsSL https://raw.githubusercontent.com/yusing/mekugi/main/install.sh | sh
```

Add `~/.local/bin` to your `PATH` if needed. Release archives are also on
[GitHub Releases](https://github.com/yusing/mekugi/releases/latest) (or from a
[Build and release run][ci]). Non-glibc Linux environments (such as Alpine)
require a source build.

[ci]: https://github.com/yusing/mekugi/actions/workflows/release.yml

### Build from source

Requires **Go 1.27+** (CGO enabled), a C/C++ toolchain, **Make**,
**Bun 1.4.4+**, **Node.js 24.21+**, **Python 3.11+**, and **Rustup**. See
[native build dependencies](docs/contributing/development.md#native-app-server)
for Linux libraries and the pinned Rust toolchain.

```sh
git clone https://github.com/yusing/mekugi.git
cd mekugi
git submodule update --init runtime/codex
make install
```

Add `$GOBIN` (or `$(go env GOPATH)/bin`) to your `PATH`. Run `make uninstall`
to remove installed executables and the bundled model catalog. Start a new
session after updating.

## Quick start

```sh
mekugi codex                   # Native OpenAI (uses `codex login`)
mekugi -p work                 # Custom Responses provider profile
```

Put Mekugi flags **before** `codex` or Codex arguments. Bare `mekugi` requires
a [custom provider profile](docs/guide/configuration.md#codex-profile-imports).
Closing the UI leaves threads running and prints resume/replay commands. See
[Launch and automation](docs/guide/launch.md) for headless runs and flags.

| To | Do |
| --- | --- |
| Steer the running turn / queue next | `Enter` / `Tab` |
| Interrupt | `Esc` |
| Show Main / toggle Diff / focus Agents | `Ctrl-B`, then `1` / `2` / `3` |
| Inspect or stop a running command | `/ps` |
| Ask a side question / run a one-off task | `/btw`, `/oneoff` |
| Compact, fork, or rewind | `/compact`, `/fork`, `/rewind` |
| Show all shortcuts | `?` in an empty draft |

## Guides

- [Terminal UI](docs/guide/terminal-ui.md): panes, composer, live diffs, Agents sheet, and output dialogs.
- [Sessions and recovery](docs/guide/sessions.md): resume, fork, rewind, compaction, and replay storage.
- [Agent tools and change review](docs/guide/agent-tools.md): `mcat`, `mrun`, `msymbol`, `mfinspect`, and `mchanges`.
- [Approvals and guards](docs/guide/approvals.md): Codex approvals, remote-write/sudo guards, and GPG passphrase entry.
- [Server, connector and browser](docs/guide/remote.md): remote control, browser UI, and Docker deployment.
- [Providers](docs/guide/providers.md): custom Responses providers.
- [Launch and automation](docs/guide/launch.md): launch options, execution policy, and headless runs.
- [Configuration and metrics](docs/guide/configuration.md): settings file, flags, metrics, and integrations.

## Documentation

[Product requirements and design](docs/product.md) ·
[Documentation index](docs/index.md) ·
[Contributing](CONTRIBUTING.md) ·
[Controlled comparisons](docs/benchmarks.md) ·
[Codex end-to-end checks](docs/codex-router-e2e.md)

For controlled comparisons against stock Codex, see
[codex-setup-ab](https://github.com/yusing/codex-setup-ab).

## License

MIT. See [LICENSE](LICENSE).
