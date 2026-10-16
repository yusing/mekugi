# Provider routing and native-agent continuity

## Authority and ownership

Third-party models serve Main and native child agents without requiring child-only
headers. Codex owns agents, messages, tools, permissions, sandboxing, interruption
and continuation. The application selects and pins provider policy; provider
implementations own credentials, metadata and inference transport. The
[Codex adapter](../../backends/codex/responses/SPEC.md) owns wire translation and
stream normalization. No provider adapter creates an agent runtime, decrypts
foreign history or substitutes its own transcript or registry.

[Launch](../../apps/mekugi/internal/launch/SPEC.md) owns private catalog preparation
and invocation model namespaces. Disabled or wrong-mode routes fail locally before
inference. Enabling a third-party route does not change ordinary OpenAI request
handling. Third-party continuation sends complete history over HTTP; WebSocket
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

Unsupported or foreign encrypted history is a local compatibility error, not a
retryable transport failure, and directs the caller to fresh context. Native call
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
