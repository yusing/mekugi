# mekugi

A mekugi is the small peg that pins a Japanese sword's handle to the blade.
Take it out and the handle comes off. Leave it in and the blade is still the
blade.

Mekugi keeps stock Codex agents working from current, compact context across
long sessions, compaction, and delegates, and lets you see what they are doing,
what they are about to do, and what they have done. Codex keeps editing,
execution, the sandbox, permissions, command sessions, and patch review.
Invocation overrides leave Codex configuration unchanged.

[Why Mekugi](#why-mekugi) · [Install](#install) · [Quick start](#quick-start) ·
[Guides](#guides) · [Documentation](#documentation)

## Why Mekugi

### Agents work from current, compact context

- **Compaction that rereads sources.** When a thread compacts, the agent writes its
  handover and [selects the files, skills and captured changes](modules/agentguidance/SPEC.md#compaction-context)
  its unfinished work needs. Mekugi reads them again before the conversation
  continues, so work resumes from current source text rather than a summary of it.
- **Agent-chosen checkpoints.** Agents can [compact at safe checkpoints](modules/threadcontrol/SPEC.md#operations-and-admission)
  and continue, or compact while waiting for delegates. [Measured usage reminders](modules/agentguidance/SPEC.md#context-usage-notices)
  arrive from halfway full when usage is measured.
- **Delegates start prepared.** [Fresh agents start with selected files, skills and captured changes](modules/threadcontrol/SPEC.md#prepared-children-and-guidance)
  already loaded. Parents wait for one or all agents without polling; delegates
  can coordinate their own children and reply with a short summary and details.
- **Facts up front.** Codex agents receive an [observed OS and Git snapshot](modules/agentguidance/SPEC.md#environment-snapshot),
  and attached files and skills are marked as reusable, so neither is rediscovered.
- **Fewer output tokens, nothing lost.** [Session helpers](docs/guide/agent-tools.md)
  bound reads, symbol lookups, outlines and command output, and every incomplete
  result names the exact command that continues it.
  [Repeated output](modules/outputdeduplication/SPEC.md) becomes a reference to
  earlier visible text, and [marked instruction blocks](docs/guide/configuration.md#troubleshooting-and-integrations)
  can be omitted. Full results stay in evidence and the UI.

### Exact handoffs and undo

- **Captured changes by ID.** Every observed edit gets a short ID. Agents pass
  IDs and ranges to reviewers and across compaction, so a reviewer inspects
  exactly the delegated work rather than a worktree diff mixed with other agents'
  and your edits.
- **Undo exactly what you reject.** When you object to part of the work, the
  agent can [revert the captured edits behind it](modules/changehistory/SPEC.md#revert-and-apply),
  optionally limited to specific paths, without `git restore`. Later and unrelated
  edits, including your uncommitted work, are merged rather than discarded, and
  the revert can be reapplied. [`/rewind`](docs/guide/sessions.md#resume) offers
  the same reversal when branching before an earlier turn.

### See what agents are doing, will do, and have done

- **Live activity with its reasons.** Operations appear under the reasoning that
  led to them. Delegates appear with their assignment and reply summary in Main,
  and with progress and metrics in the [Agents pane](docs/guide/terminal-ui.md#agents-pane).
  Open attachable children to read their transcripts and steer them.
- **Catch mistakes before they land.** [Edits stream into the Diff pane](docs/guide/terminal-ui.md#live-diff-pane)
  while the agent writes them. Steer or interrupt the turn, [stop one running command](docs/guide/terminal-ui.md#composer),
  or deny [remote writes and sudo](docs/guide/approvals.md) before they run.
- **Review what happened.** Open saved edits by hunk, [searchable per-command output](docs/guide/terminal-ui.md#output-dialog),
  compaction summaries, and [usage, throughput and cost](docs/guide/configuration.md#metrics).
  Missing evidence and provisional previews are labeled. Past sessions can be
  [inspected](docs/contributing/development.md#inspect-a-session) or
  [replayed](docs/contributing/development.md#replay-a-session).

### Work from anywhere

- **Server-owned threads.** Threads keep running when the terminal or browser
  closes. [Control them from another machine](docs/guide/remote.md), including
  phones, through an authenticated connector.
- **Branches.** [Resume, fork, or rewind](docs/guide/sessions.md) conversations.
- **Providers and delegates.** Use [Grok, OpenCode Go and Zen](docs/guide/providers.md),
  and [Claude delegates](docs/guide/providers.md#claude-delegates) through the same
  coordination tools.
- **Composer.** Attach files and skills, ask side questions, run one-off tasks,
  recall presets, and change model, reasoning, tier and permissions in session.
  See the [composer](docs/guide/terminal-ui.md#composer).

Savings from bounded output, deduplication and source reloads don't guarantee
better results on every task. For controlled comparisons, use
[codex-setup-ab](https://github.com/yusing/codex-setup-ab). The
[product principles](docs/product.md#product-principles) explain these choices.

## Install

Requirements:

- **Codex CLI** on the execution machine.
- Built-in helpers need neither Node.js nor Bun; semantic lookup uses the language
  servers listed in [agent tools](docs/guide/agent-tools.md#wrapped-session-helpers).
- Any interpreter your agent picks, such as `python3`, on the executor's `PATH`.
- Claude delegates additionally need Bun and an authenticated Claude Code
  installation on the execution machine's `PATH`.

### Release binaries

| Platform | Archive | Minimum OS |
| --- | --- | --- |
| Linux x86-64 | `mekugi_linux_amd64.tar.gz` | glibc 2.39, such as Ubuntu 24.04 |
| Linux ARM64 | `mekugi_linux_arm64.tar.gz` | glibc 2.39, such as Ubuntu 24.04 |
| macOS Apple Silicon | `mekugi_darwin_arm64.tar.gz` | macOS 15 |

The [installer](install.sh) selects your platform, verifies the release checksum,
and installs `mekugi` and `mekugi-exec` into `~/.local/bin`, replacing existing copies:

```sh
curl -fsSL https://raw.githubusercontent.com/yusing/mekugi/main/install.sh | sh
```

Add `~/.local/bin` to your `PATH` if needed. Archives are also on
[GitHub Releases](https://github.com/yusing/mekugi/releases/latest); they need
no Go or C toolchain. Linux releases need glibc and do not run on stock Alpine
Linux; use a source build for other environments.

### Build from source

Source installs require **Go 1.27+**, CGO enabled, a C toolchain, **Make**,
**Bun 1.4.4+**, and **Node.js 24.21+** for browser assets:

```sh
git clone https://github.com/yusing/mekugi.git
cd mekugi
make install
```

Add `$GOBIN`, or `$(go env GOPATH)/bin` if that is unset, to your `PATH`.
`make uninstall` removes only the two binaries. Running sessions keep their
worker executable, so start a new session to pick up an update.

## Quick start

```sh
mekugi codex                    # Codex models, using `codex login`
mekugi grok -m grok-4.7         # Grok credentials
mekugi -m opencode-go:glm-5.3   # OpenCode credentials
```

Mekugi flags go **before** `codex`, `grok`, or Codex arguments. The launch starts
a local server when needed and attaches the terminal UI. Closing the UI leaves
the thread running; exit prints resume and replay commands. Codex's approval and
sandbox policy applies; the remote-write guard needs unsandboxed execution.
Mekugi needs Codex's JavaScript `exec`; custom inference endpoints and `--oss`
are unsupported. See [launch details](docs/guide/launch.md).

| To | Do |
| --- | --- |
| Steer the running turn / queue for the next | Enter / Tab |
| Interrupt | Esc |
| Show Main / toggle Diff / focus Agents | `Ctrl-B`, then `1` / `2` / `3` |
| See running commands and stop one | `/ps` |
| Compact, fork, or rewind | `/compact`, `/fork`, `/rewind` |
| Show shortcuts | `?` in an empty draft |

## Guides

- [Terminal UI](docs/guide/terminal-ui.md): panes, composer, live diffs, Agents pane and output dialogs.
- [Sessions and recovery](docs/guide/sessions.md): resume, fork, rewind, compaction and replay storage.
- [Agent tools and change review](docs/guide/agent-tools.md): `mcat`, `mrun`, `mread`, `msymbol`, `inspect_file` and `mchanges`.
- [Approvals and guards](docs/guide/approvals.md): Codex approvals, remote-write and sudo guards.
- [Server, connector and browser](docs/guide/remote.md): remote control, browser UI and Docker deployment.
- [Providers and delegates](docs/guide/providers.md): Grok, OpenCode and Claude delegates.
- [Launch and automation](docs/guide/launch.md): launch commands, execution policy and headless runs.
- [Configuration and metrics](docs/guide/configuration.md): settings file, options, metrics and troubleshooting.

## Documentation

[Product principles](docs/product.md) · [Documentation index](docs/index.md) ·
[Contributing](CONTRIBUTING.md) · [Comparisons](docs/benchmarks.md) ·
[Codex end-to-end checks](docs/codex-router-e2e.md)

## License

MIT. See [LICENSE](LICENSE).
