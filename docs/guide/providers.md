# Providers and delegates

Use native OpenAI models with `mekugi codex`, or configure a custom Responses
provider through a [profile import](configuration.md#codex-profile-imports).
Claude delegates retain their separate execution backend and authentication.

## Custom Responses providers

Run `mekugi -p NAME` with the provider definition and model in
`NAME.config.toml` under the admitted Codex home. Supply the endpoint's client
credential through its configured environment variable or native authentication
command. Model identities and available capabilities come from that provider;
Mekugi does not maintain a replacement multi-provider catalog or pricing table.
Unknown prices remain unknown.

A configured Responses endpoint is not a promise of support for every model,
reasoning format, image or auxiliary tool. Unsupported operations retain the
provider's error. CLIProxyAPI integration and release-specific validation are
not yet provided by this migration.

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
