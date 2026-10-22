# Provider routing and native-agent continuity

## Authority and ownership

Third-party models serve Main and native child agents without requiring child-only
headers. Codex owns agents, messages, tools, permissions, sandboxing, interruption
and continuation. The application inference gateway selects and pins provider
policy, binds HTTP and dedicated WebSocket exchanges, and owns downstream socket lifetime. Provider
implementations own credentials, metadata and upstream transport. The
[Codex adapter](../../backends/codex/responses/SPEC.md) owns wire translation and
stream normalization. No provider adapter creates an agent runtime, decrypts
foreign history or substitutes its own transcript or registry.

[Launch](../../apps/mekugi/internal/launch/SPEC.md#model-catalog) owns private
catalog assembly, preparation and invocation model namespaces. Routing consumes
provider metadata independently of that startup projection. Disabled or wrong-mode
routes fail locally before inference. Enabling a third-party route does not change ordinary OpenAI request
projection. OpenAI-only WebSocket connections establish the provider handshake
before accepting the host connection, preserving the provider's
`x-reasoning-included` signal, including its absence. Failed upgrades retain the
provider's HTTP status and bounded error body. Setup obeys cancellation and the
configured response-start timeout and generates no inference.

Mixed-provider connections retain deferred provider selection and cannot relay
provider handshake-only signals through their already-accepted host socket.
They never connect to OpenAI merely to serve a third-party request. HTTP responses
preserve provider headers. Third-party continuation sends complete history over HTTP; WebSocket
prewarm does not generate inference, and disconnect cancels provider work.

## Plaintext collaboration

Ordinary Mekugi turns expose native collaboration as `mekugi_collaboration`, with
no OpenAI encryption annotation. Response calls recover the original native
`collaboration` identity. Spawn, send-message and follow-up calls carry the explicit
empty `encrypted_function_args` marker required for plaintext host delivery.
Replay maps bridge-produced calls back to their provider-visible identity;
historical calls with nonempty encryption markers retain their native namespace
and metadata. Ordinary and additional-tool catalogs follow the same rule.

Guidance in the projected namespace names its dispatch surface. Top-level
instructions, developer content and user messages remain unchanged, including
multipart content and fenced examples. The reserved OpenAI schema is not
modified in place. Readable new assignments support native Activity display;
encrypted message bodies remain unavailable. Passthrough, prewarm and auxiliary
turns without a third-party provider retain their existing behavior.

Spawn descriptions expose configured models and their fresh-context requirements
beside existing model, context and reasoning arguments. They preserve native
role restrictions, descriptions, required fields, validation and defaults, add no
missing arguments, and never rewrite submitted arguments. Callers explicitly
select fresh context and supply a self-contained assignment.

## Selected adaptation policy

The gateway resolves provider capabilities before calling the backend codec.
Request preparation, execution admission, retained evidence, accounting and notices
remain application consumers. The shared application
[request executor](../../apps/mekugi/internal/requestexecution/SPEC.md) runs both
HTTP and socket attempts; transport does not acquire a second execution or
observation authority.

The gateway owns invocation-local service-tier selection. Confirmed choices are
scoped by host thread and the exact requested model, ahead of configured model
defaults and the request's tier. Requested model identities remain unchanged;
`fast` is forwarded and displayed as `priority`. Request preparation, Main and
child captions use this same policy without rewriting confirmed host settings.

### Grok

Grok uses Chat Completions. Non-null `max_output_tokens` rejects locally: Chat completion limits exclude
reasoning and cannot enforce a Responses total-output budget. Grok completion and
reasoning counts combine into Responses output tokens, with reasoning retained as
a breakdown rather than charged twice. Transport metrics describe the actual Chat exchange.

Plaintext reasoning streams as Responses summaries but is not replayed to Grok.
Encrypted reasoning/messages, opaque file IDs and provider-only history reject
before inference. Fresh-context spawning avoids foreign encrypted history.

### OpenCode

[Launch](../../apps/mekugi/internal/launch/SPEC.md#model-catalog) owns the
Codex catalog projection. Explicit supported reasoning efforts pass as Chat
`reasoning_effort`, Responses `reasoning.effort` or Messages
`output_config.effort`; structured output can coexist with effort. An absent or
unsupported inherited value leaves provider defaults alone. `none` is forwarded
only when advertised; toggle-only or token-budget-only controls are not efforts.

Chat reasoning summaries are replayed on the corresponding assistant message,
including tool-free and resumed turns. Messages thinking/signature/redacted blocks
and Responses reasoning items use versioned, service/model-bound opaque envelopes
that survive JSON persistence. They restore only on that route; foreign encrypted history fails locally.
Visible Chat, Messages and Responses reasoning streams as received, including
completed-item text when no deltas exist. The backend's item lifecycle preserves
its order relative to the answer. Messages/Responses visible summaries belong to
the same opaque item; closing without a replay envelope retains no summary-only
reasoning, which those formats cannot restore.

OpenCode completion counts already include reasoning; Responses output retains
that total and exposes reasoning only as a breakdown. Missing usage remains
missing. Messages input includes uncached input plus cache reads/writes, and its
output includes thinking. Messages requires a generation ceiling, defaulting to
32,768 tokens; an explicit total-output budget maps to `max_tokens`. Responses
preserves the requested total budget; Chat rejects incompatible total budgets.

Transport capture keeps opaque reasoning separate from user-visible text. Each
exchange retains its selected [price metadata](../../providers/opencode/SPEC.md#reference-prices)
through accounting, even if the catalog refreshes during the request.

## Isolation and failure

Provider credentials, refresh, clients and redirects remain isolated from OpenAI.
Codex credentials and internal account/thread headers are excluded unless a provider
contract defines a sanitized replacement. Third-party credentials never reach
OpenAI. Incoming requests retain the ordinary Codex authentication boundary.

Rejected inference preserves the actual provider HTTP status and error details in
HTTP/WebSocket errors, terminal failures and notices. Reads are bounded to 8 KiB
and five seconds; unreadable or oversized bodies say so. JSON failures retain
available name, type or code alongside the buffered payload; other failures retain
available text. Caller-facing errors and private failure records do not redact or
further truncate those details. Sanitized metrics and capture keep classification
only, never credentials, prompts or provider error text. Explicit Chat, Messages
and Responses stream errors follow this policy even for non-stream clients.

Unsupported or foreign encrypted history is a local compatibility error with
HTTP/WebSocket status 400, not a retryable transport failure, and directs the caller to fresh context. Native call
identities and argument bytes survive translation and replay. Complete arguments
are validated before executable calls are exposed; malformed terminals, unfinished
calls and truncated streams cannot become successful results. Cancellation and
inactivity limits propagate upstream. Diagnostic identity comes from the selected
provider, not another provider's error rewritten afterward.

## Acceptance

Consuming application tests cover route admission, catalog/authentication isolation,
plaintext call restoration, exact arguments, provider errors and cancellation.
Backend cases cover framing and executable-call integrity. Installed-host checks
separately establish that Codex executes a translated call once and consumes its
result through child completion, messaging and follow-up. Offline checks alone do
not establish that host boundary.
