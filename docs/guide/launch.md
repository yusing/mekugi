# Launch and automation

Start interactive sessions, choose their execution policy, and run headless automation.

## Start Mekugi

| Launch | Models | Authentication |
| --- | --- | --- |
| `mekugi` | Third-party `grok:...` and `opencode*:` IDs | [Grok](providers.md#grok-models) or [OpenCode](providers.md#opencode-go-and-zen) credentials |
| `mekugi grok` | Plain Grok IDs, such as `grok-4.7` | Grok credentials |
| `mekugi codex` | Codex models only | `codex login` with ChatGPT authentication |

Standalone requires third-party credentials. Its default follows Grok's model selection
when authenticated, otherwise the first available OpenCode Go model, then Zen. Mekugi
flags go **before** `codex`, `grok`, or standalone Codex arguments.

Codex's approval/sandbox policy applies. The UI's [remote-write guard](approvals.md#approvals) needs
unsandboxed execution: configure `sandbox_mode="danger-full-access"`, or use `--yolo` to
disable Codex approvals/sandboxing while keeping the guard. To disable Mekugi's
remote-write and sudo guarding while retaining Codex policy, use `--shell-guard=false`.

```sh
mekugi codex
mekugi grok -m grok-4.7
mekugi -m opencode-go:glm-5.3
mekugi exec "Explain this repository"
```

Interactive launches start the local server when absent and attach the [UI](terminal-ui.md).
Closing the UI leaves its thread running; exit prints resume/replay commands.
Ctrl-C cancels the startup wait. Noninteractive commands retain Codex's
arguments/output/exit status.
Mekugi needs JavaScript `exec`. Custom inference endpoints and `--oss` are unsupported.
See [launch limits](../../apps/mekugi/internal/launch/SPEC.md) and
[request integration](../contracts/responses-transport.md); invocation overrides leave config unchanged.

## Headless execution

```sh
mekugi headless --yolo < prompt.txt
```

Headless requires `--yolo` and one nonempty stdin prompt up to 16 MiB; model/`-c`
options are accepted, resume/positional prompts are not. It supports the same
Thread Control and child preloads as the interactive client, and exits after Main's
logical work and required descendants complete. Questions/approvals fail; Ctrl-C stops the family. Stdout
JSONL contains private host messages and a completion event;
diagnostics go to stderr. Run/shutdown failure exits nonzero. `codex exec` is unchanged.
