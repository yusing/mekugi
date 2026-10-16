# Grok provider

## Ownership

Own API-key selection, OAuth credential reads, trusted discovery, token refresh,
credential publication, and authenticated inference transport. The application
selects the credential path and configured HTTP transport. Grok owns interactive
login. The application applies the routing and Codex adaptation policy below. Shared
authority and failure semantics belong to [provider routing](../../docs/contracts/provider-routing.md).

## Models and invocation

Dedicated `mekugi grok` uses plain `grok-4.5`, `grok-4.6`, `grok-4.7` and
`grok-4.7-build-fast` IDs. Standalone `mekugi` uses `grok:`-prefixed IDs alongside
configured OpenCode models. Both require Mekugi response mode and no Codex login;
`mekugi codex` retains OpenAI behavior and rejects Grok requests locally. Wrong
namespaces and the old `--grok` flag also reject locally.

Explicit invocation model selection wins. Otherwise `[models].default` in
`~/.grok/config.toml` selects the model, falling back to `grok-4.7` when absent or
empty. An unreadable/malformed file fails unless an explicit model bypasses it.
The application adds the standalone namespace. Automatic defaults do not overwrite
saved models during interactive resume or switching; explicit selections do.
Noninteractive resume/fork defaults follow the [launch contract](../../apps/mekugi/internal/launch/SPEC.md).

The private Codex catalog uses bundled native v2 instruction/executor metadata,
text/image input, a 500,000-token context window, and low/medium/high/xhigh effort
with high as default. It excludes OpenAI entries and does not inherit Responses
Lite, hosted search, service tiers or upgrade schedules. Existing Grok entries are
rebuilt rather than duplicated. Spawn guidance preserves native argument and role
constraints under the shared routing contract.

## Authentication

A nonempty trimmed API key takes precedence over the selected OAuth store and
requires no store access. OAuth supports the standard `https://auth.x.ai` Grok
public client. Custom enterprise issuers and untrusted discovery endpoints are
rejected. The provider returns credentials for the public xAI API or Grok CLI
proxy; application routing follows the invocation namespace below.

Expired or near-expiry tokens refresh under Grok's cross-process advisory lock.
Refresh re-reads credentials under that lock, atomically publishes rotated tokens,
and preserves unrelated accounts and fields. Rejected-token refresh reuses a
newer token already published by another lock holder. OAuth discovery and token
requests never follow redirects.

## Inference

Inference sends the supplied encoded request only to the selected credential
destination. It never forwards Codex credentials or internal headers. API-key
requests use the public xAI API; OAuth requests use the Grok CLI proxy and carry
the selected model override. Build Fast requires OAuth.

A rejected OAuth token closes the first response before refreshing and retries
once with the same request bytes. API-key requests do not retry authentication.
Inference never follows redirects. Transport failures retain their underlying
cause, including cancellation after refresh. Final rejected responses and accepted
body lifetime use the shared [HTTP exchange](../../libraries/httptransport/SPEC.md).
Codex response framing belongs to the backend adapter, not this transport.

## Codex adaptation policy

The Chat adapter preserves supported text/images, plaintext agent messages,
parallel custom/function calls and results, structured output, effort and usage.
Unsupported provider tools, encrypted reasoning/messages, opaque file IDs and
provider-only history fail before inference. Hosted OpenAI search is unavailable
and the model is told so. Fresh-context spawning avoids foreign encrypted history;
compatibility failures use HTTP/WebSocket status 400.

Non-null `max_output_tokens` rejects locally: Chat completion limits exclude
reasoning and cannot enforce a Responses total-output budget. Grok completion and
reasoning counts combine into Responses output tokens, with reasoning retained as
a breakdown rather than charged twice. Provider-returned model aliases remain
unchanged; transport metrics describe the actual Chat exchange.

Wire aliases cover namespaces and reserved names consistently across catalogs,
tool choice and replay. In particular, native `wait` is never sent under bare
`wait`, and its advertised `yield_time_ms` and `max_tokens` are integers. Responses
restores the original names and exact submitted arguments. Plaintext reasoning
streams as Responses summaries under the backend item lifecycle, but those
unencrypted summaries are not replayed to Grok.

## Lifetime and failure

Authentication is invocation-local. HTTP clients are copied before enforcing
redirect rejection; the caller's client is unchanged. Refresh is bounded and
serialized through the existing advisory lock, with a credential re-read after
acquisition. Unusable credentials fail without inventing a login or changing
another provider's authentication.

## Acceptance

Provider tests cover shared-store preservation and rotation, concurrent refresh,
trusted discovery, API-key precedence, and redirect rejection. Application tests
cover startup selection, one rejected-token retry, Build Fast eligibility,
credential isolation and cancellation through actual inference. Same-process lock tests do not
establish interoperability with a concurrently running Grok CLI. Router and backend
cases cover both model namespaces, defaults/resume, catalog constraints, images,
parallel calls, aliases, structured output, unsupported history, reasoning/usage
and incomplete streams. Installed-host execution evidence remains separate.
