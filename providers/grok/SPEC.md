# Grok authentication and inference transport

## Ownership

Own API-key selection, OAuth credential reads, trusted discovery, token refresh,
credential publication, and authenticated inference transport. The application
selects the credential path and configured HTTP transport. Grok owns interactive
login. Native model selection and protocol policy remain with the
[Grok route](../../doc/spec/grok.md).

## Authentication

A nonempty trimmed API key takes precedence over the selected OAuth store and
requires no store access. OAuth supports the standard `https://auth.x.ai` Grok
public client. Custom enterprise issuers and untrusted discovery endpoints are
rejected. The provider returns credentials for the public xAI API or Grok CLI
proxy; application routing policy belongs to the linked route specification.

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
establish interoperability with a concurrently running Grok CLI.
