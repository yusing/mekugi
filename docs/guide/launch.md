# Launch and automation

Start interactive sessions, choose their execution policy, and run headless automation.

## Start Mekugi

| Launch | Models | Authentication |
| --- | --- | --- |
| `mekugi -p NAME` | Models from the selected Responses provider | [Profile-defined credentials](configuration.md#codex-profile-imports) |
| `mekugi codex` | Native OpenAI models | `codex login` with ChatGPT authentication |

Bare `mekugi` requires an explicitly configured custom Responses provider profile.
Mekugi flags go **before** `codex` or the remaining Codex arguments.

Codex's approval/sandbox policy applies. The UI's [remote-write guard](approvals.md#approvals) needs
unsandboxed execution: configure `sandbox_mode="danger-full-access"`, or use `--yolo` to
disable Codex approvals/sandboxing while keeping the guard. To disable Mekugi's
remote-write/sudo guarding and GPG passphrase entry while retaining Codex policy, use `--shell-guard=false`.

```sh
mekugi codex
mekugi -p work
```

Interactive launches start the local server when absent and attach the [UI](terminal-ui.md).
Closing the UI leaves its thread running; exit prints resume/replay commands.
Ctrl-C cancels the startup wait. Use explicit headless execution below for automation;
redirected ordinary launches and stock CLI subcommands are not supported through Mekugi.
Mekugi needs JavaScript `exec`. Custom Responses endpoints require a [profile import](configuration.md#codex-profile-imports).
`--oss` remains unsupported.
See [launch limits](../../apps/mekugi/internal/launch/SPEC.md) and
[request integration](../contracts/responses-transport.md); invocation overrides leave config unchanged.

## Headless execution

```sh
mekugi codex headless --yolo < prompt.txt
```

Headless requires `--yolo` and one nonempty stdin prompt up to 16 MiB; model/`-c`
options are accepted, resume/positional prompts are not. It supports the same
Thread Control and child preloads as the interactive client, and exits after Main's
logical work and required descendants complete. Questions/approvals fail; Ctrl-C stops the family. Stdout
JSONL contains private host messages and a completion event;
diagnostics go to stderr. Run/shutdown failure exits nonzero.
