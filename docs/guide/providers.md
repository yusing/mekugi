# Providers and delegates

Use third-party models and Claude delegates alongside Codex.

## Grok models

Use `grok login --oauth` or `XAI_API_KEY` (takes precedence); Codex credentials stay separate.
The default follows `[models].default` in `~/.grok/config.toml`, then the latest standard model (currently `grok-4.7`);
`-m`/`-c model=...` overrides it. Standalone IDs need `grok:`; `mekugi grok` uses plain IDs.
Subagents inherit full history by default; `fork_turns="none"` starts fresh. Build Fast needs OAuth;
encrypted OpenAI history cannot switch to Grok. See [Grok requirements](../../providers/grok/SPEC.md).

## OpenCode Go and Zen

```sh
OPENCODE_GO_API_KEY='your-key' mekugi -m opencode-go:glm-5.3
OPENCODE_ZEN_API_KEY='your-key' mekugi -m opencode-zen:kimi-k3
```

Keys can also live in [settings](configuration.md#mekugi-settings). `OPENCODE_API_KEY` overrides file
keys; per-service variables override it, and an empty value disables that service.
Models/prices refresh hourly; the picker updates next launch. See
[OpenCode](../../providers/opencode/SPEC.md).

## Claude delegates

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
recipes bundle it automatically. See the [Claude backend](../../backends/claude/SPEC.md).
