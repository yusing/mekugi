# Providers

Use native OpenAI models with `mekugi codex`, or configure a custom Responses
provider through a [profile import](configuration.md#codex-profile-imports).

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
