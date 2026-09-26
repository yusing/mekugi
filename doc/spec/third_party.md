# Third-party native-agent routing

## REQ-THIRD-PARTY-001 — Third-party native-agent routing

Third-party models are routes for main agents and native child agents. Child-specific headers and
metadata are not required for routing. Codex owns spawning, listing, messaging, waiting,
interruption, follow-up, tool execution, permissions, sandboxing, and lifecycle. The router selects
providers and adapts their protocols; it never creates a substitute agent process or executes a
tool itself.

When any third-party route is enabled, the wrapper runs `codex debug models` with the invocation's
configuration and working-directory selectors, writes a private session catalog, and enforces its
`model_catalog_json` path in the final invocation-only config layer. The selected Codex catalog must
provide a native v2 template. Named Codex profiles (`--profile`/`-p`) and
`exec --ignore-user-config` are rejected before catalog loading because the catalog command cannot
honor those configuration modes. The default configuration and an explicit
`-c model_catalog_json` remain supported. The catalog is fixed for the session and cannot be
replaced by another Codex process's shared cache. Catalog command failure, invalid output, or
private-file creation failure prevents the main Codex launch. Output is bounded to 8 MiB and
catalog preparation to one minute.

Preparation emits content-free stderr progress immediately and every ten seconds while waiting.
Interactive status is width-bounded and cleared before terminal handoff; redirected output uses
complete lines. Rendering failures stop progress only; they never cancel preparation or replace
its result. Cancellation and deadline errors take precedence over generic subprocess or
configuration advice, and private bootstrap stderr is not exposed. The private directory is mode
0700, its file is mode 0600, and both are removed on exit or launch failure. User configuration and
the shared cache are not rewritten by Mekugi; Codex retains its normal catalog-command behavior.
`/v1/models` returns the upstream catalog unchanged, without third-party injection or a synthetic
ETag, using the memory-only session cache described in [router diagnostics and lifecycle](router.md).

In ordinary Mekugi turns, the provider-visible collaboration namespace is
`mekugi_collaboration`, independently of which third-party provider is enabled. Its message schema
has no OpenAI encryption annotation. Response calls are restored to the original native
`collaboration` namespace; spawn, send-message, and follow-up calls carry the explicitly empty
`encrypted_function_args` marker required by Codex for plaintext delivery. Replay consistently maps
bridge-produced native calls back to their provider-visible identities. Historical calls with
nonempty encryption markers retain their native namespace and encryption metadata. Both ordinary
and additional-tool catalogs are covered. When this namespace is exposed, the complete pinned
native dispatch instruction in top-level and developer-message text uses the same namespace,
including multipart content. Caller wording, fenced examples, and user messages remain unchanged.
The reserved OpenAI collaboration schema is not modified in place because the provider rejects that
operation. User-visible agent lifecycle remains native.

Plaintext projection makes new assignment payloads readable to the router, allowing journal answer
association. It does not decrypt existing encrypted agent messages; a new plaintext follow-up is
required to attach those tasks. Ordinary passthrough, prewarm, and auxiliary turns without a
third-party provider remain unchanged.

Provider credentials, refresh, redirects, and clients remain isolated from OpenAI authentication.
Codex credentials and internal account or thread headers are not forwarded unless a provider
contract explicitly defines a sanitized replacement. Third-party credentials never reach OpenAI.

Rejected third-party inference requests preserve the provider's HTTP status and error details in
HTTP/WebSocket errors and user-facing failure notices instead of replacing them with authentication
advice. Error reads are limited to 8 KiB and five seconds; unreadable or oversized bodies get an
explicit explanation. JSON error messages retain their name, type, or code when present alongside
the original buffered payload; other bodies retain the available text. Error display and failure
records do not redact credentials or truncate the resulting error string. Sanitized capture and
metrics retain only the HTTP classification, never credentials, prompts, or provider error text.
The ordinary Codex
authentication boundary still applies to incoming requests.

Provider error events in Chat, Responses, and Messages streams preserve their actual
error details in caller-facing terminal events and failure notices, including when a
non-stream request consumes a provider stream. The buffered error payload is retained without
display redaction or truncation; sanitized capture and metrics retain only classifications.

Adapters preserve native tool identities and validate complete tool arguments before exposing an
executable call. Unsupported or encrypted history fails locally before a provider request and
directs the caller to a compatible fresh context. Provider adapters preserve cancellation and do
not report truncated streams, unfinished calls, or malformed terminals as completed responses.

Provider-specific behavior and acceptance cases belong to the [Grok](grok.md) and
[OpenCode](opencode.md) contracts.

Acceptance:

1. Disabled routes preserve existing OpenAI behavior, and unconfigured third-party model names fail
   locally without a provider request.
2. JSON/SSE projection and replay preserve native agent call IDs and plaintext handoffs while Codex
   remains the sole owner of native agent lifecycle and tool execution.
3. Credentials, request headers, diagnostics, and metrics remain isolated by provider.
4. Unsupported history and incomplete or invalid executable calls fail before execution.
