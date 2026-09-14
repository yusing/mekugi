# Tool registry and executor carrier boundary

## CTR-PLUGIN-001 — Tool registry and executor carrier boundary

The router owns discovery, complete-registry validation, stable registration order, global
tool-name ownership, immutable process-lifetime registry state, and fail-before-serve
behavior. A normalized contribution contains exact model-visible specification, bounded
input parsing, typed translation, and optional executor dispatch. Plugin code receives no
workspace authority or credential interface.

Portable core capabilities are versioned separately from plugin declarations and are shared
by built-in and configured contributions. They own only deterministic source, syntax, and
framing semantics. Workspace canonicalization, process execution, transport envelopes,
carrier policy, and replay remain with their existing owners.

One renderer owns each supported Codex carrier shape. Plugins select a typed carrier and
payload but cannot construct outer response envelopes, IDs, replay items, or nested quoting.
Codex validates and executes the resulting carrier under its own permissions and lifecycle.
Unsupported or malformed translation fails routing rather than approximating a result.

Executable contributions use an authenticated immutable snapshot. Children verify registry
identity and never rediscover live configuration, so file changes take effect only after a
new launch. Process, wrapper, retained-script, and runtime-locator cleanup is limited to
resources created and leased by that router. Retention does not grant a new filesystem or
process authority.

Response restoration uses registry identity across JSON, streaming, native, and replay paths.
A completed mapping is durable before its carrier is exposed; replay verifies it byte for
byte and restores the original model-visible call without executing effects. Generic plugin
history cannot enter edit recovery. Diagnostics remain separate from executor output and
retain exact provenance.

The built-in shell is model-visible; its read, search, symbol, and inspection commands are
private to the authenticated executor. They share portable row and source semantics while
retaining distinct selection owners. The router never fabricates their results, adds
standalone frontends, or turns their shell history into edit-recovery ancestry.

Mixed edit and shell input uses one sequential Codex-owned carrier. Edit segments are
translated against the current filesystem immediately before Codex applies them; shell
segments reuse ordinary execution and native continuation. Private checkpoints retain
progress, revisions, and repair insertions without applying workspace effects. Resume,
retry, repair, and acceptance continue only the retained plan and never report translation
or checkpoint retention as successful execution.
