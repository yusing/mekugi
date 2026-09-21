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
new launch. Process, wrapper, and runtime-locator cleanup is limited to
resources created and leased by that router. Retention does not grant a new filesystem or
process authority.

Response restoration uses registry identity across JSON, streaming, native, and replay paths.
A completed mapping is durable before its carrier is exposed; replay verifies it byte for
byte and restores the original model-visible call without executing effects. Generic plugin
history cannot enter edit recovery. Diagnostics remain separate from executor output and
retain exact provenance.

The built-in shell is model-visible. `mcat` is model-private but executes only
through its session frontend. Router-native `mread` and `mrun`, plus generated
`msymbol`, use the same authenticated frontend path; hgrep and inspect_file remain
private shell commands while also exposing pinned frontends for their later cutovers.
They share portable source semantics while retaining distinct selection owners.
Codex remains the execution authority for every frontend. The router never
fabricates their results or turns command history into edit-recovery ancestry.
The `mread` frontend delegates retained output semantics to the existing managed store rather
than duplicating them in the registry. The `mrun` frontend retains Codex's foreground process
authority and owns only one bounded child execution plus completed-output retention. The
`msymbol` frontend delegates the semantic query to the generated plugin executor and adds only
the shared AX observation around that authenticated invocation.

Optional shell command-routing policy is built-in plugin code, separate from model-visible
tool declarations. The authenticated registry retains its candidate names and required
executable. The plugin transforms expanded argv only; the shell executor checks current
availability and display eligibility. The `mrun` frontend performs the same pinned check for
its one child. Each path executes the resulting argv through its existing host-owned
process lifecycle. Output selection remains downstream. Policy evaluation
never executes, retries, or replays the target command.

The `hpatch` shell command receives expanded arguments or stdin in the
existing executor. It calls the edit engine directly under Codex's shell execution
authority. The router neither evaluates shell substitutions nor applies edits during
translation. No mixed-script carrier, private control channel, or checkpoint runner
is involved.
