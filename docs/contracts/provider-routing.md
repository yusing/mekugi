# Provider routing and thread continuity

## Required provider behavior

Native OpenAI access and explicitly configured custom Responses providers preserve
Codex execution, tools, permissions, batching, continuation and per-thread usage.
Provider access does not create an agent runtime or own a second transcript.
Claude delegates retain their separate SDK execution and authentication boundary.

The application inference gateway pins the selected route and owns its HTTP and
WebSocket lifetime. Request preparation, execution admission, retained evidence,
accounting and notices remain shared consumers. Native Codex owns credential
acquisition and retries. Native OpenAI must not depend on a proxy service.

## Shared service tiers and failures

Confirmed service-tier choices are scoped by native thread and exact requested
model, ahead of configured defaults and request values. Model identities do not
change; `fast` is forwarded and displayed as `priority`. Custom routes do not
acquire OpenAI-only policy or built-in price estimates. Missing usage remains
missing and unavailable prices remain unknown.

HTTP and stream failures retain their actual provider status and available
error details, without credentials or provider-error text in sanitized metrics.
Cancellation propagates upstream. Unsupported histories are local compatibility
errors, not a retryable transport failure. Proxy-wide statistics do not replace
per-thread accounting. A compatible endpoint alone does not establish every
model or auxiliary capability.

## Profile-defined Responses routing

This route serves the
[profile import](../../apps/mekugi/internal/launch/SPEC.md#codex-profile-overrides).
The required behavior is the selected profile's actual provider/model/endpoint/auth
settings, not model-prefix substitution. The gateway mechanism below is the
chosen implementation; no-profile `mekugi codex` retains native OpenAI.

### Selection and interception

Launch captures the original provider ID and base URL before rewriting only the
host's inference address to the invocation-local gateway. Retain the original
provider ID, definition, authentication and capabilities in Codex. Do not replace
them with `openai` or `mekugi_wrap`. For the minimal custom route, the selected
provider and its base URL must be supplied by imported/explicit overrides; missing
route information fails instead of being inferred from a provider name. Other
fields retain ordinary host merging. Explicit CLI selection wins the profile.

Pin this route ahead of model-prefix classification, including model IDs that
look like retired route names. Do not discover retired credentials, catalogs or
model defaults, and do not apply built-in effort filtering or price lookup. Keep the
upstream model ID unchanged and prices unknown when no authoritative price exists.
Do not inject OpenAI-only access-program settings into a custom route.

### Native authentication and transport

Codex owns provider authentication, including `auth.command`, exact arguments,
timeout, caching/refresh, environment keys and managed login when requested.
Mekugi forwards the credentials supplied by that runtime and does not implement
another authentication command runner. Custom bearer or anonymous routes must not
require the OpenAI adapter's ChatGPT account header. No unrelated credentials are
loaded or added. The admitted caller environment belongs to the Launch contract.

Support the host's Responses wire protocol, preserving HTTP/SSE and configured
WebSocket support, query parameters, provider headers and capabilities. Do not
translate through Chat/Messages/reasoning codecs. Forward only to the
pinned endpoint, preserving base-path prefixes and encoded query values once;
strip hop-by-hop/local control headers and do not follow credential-bearing
redirects to another destination. Requests cannot choose a replacement upstream.

Extend existing HTTP/socket forwarding and request execution, not a parallel
execution pipeline. Prewarm remains non-generating, dedicated socket continuation
and interruption keep their existing lifetimes, and cancellation propagates.
Codex owns retries; no additional gateway retries. Explicit provider connect/idle
timeouts govern the corresponding upstream operation rather than being truncated
by a shorter gateway default. Explicit Mekugi operation timeouts remain outer bounds.
Provider failures retain actual status and identity under existing bounded error
handling, without being relabeled as native OpenAI failures.

Base-URL substitution and body projection are incompatible with origin/body-bound
signing and origin-bound gateway OAuth. Reject these combinations, including AWS
SigV4 and `gateway_oauth`, before inference instead of silently changing their
semantics. Ordinary command-backed bearer auth is not gateway OAuth. Managed host
policy that prohibits endpoint substitution also fails closed.

### Catalogs and auxiliary operations

Keep native unknown-model behavior; an arbitrary configured model does not require
built-in discovery or OpenAI login. Preserve explicitly supplied static
or custom-provider remote catalog metadata without rewriting tool mode or creating
derived static catalogs. The native runtime owns validation and the required
JavaScript-only execution mode. A custom remote catalog gets
an invocation-local endpoint pinned to its original catalog URL and auth semantics.

Built-in OpenAI validates remote catalog URLs against allowed HTTPS ChatGPT hosts,
so an explicit built-in OpenAI remote-catalog override cannot use this loopback
rewrite. Reject that combination in the minimal adapter. Do not rename the provider
or bypass interception. No-profile OpenAI discovery remains unchanged.

Codex owns native tool eligibility, including standalone search. Forward admitted
auxiliary calls to the selected provider and preserve its unsupported-operation
errors; never fall back to OpenAI. Backend-specific history and notes routes remain
unavailable on custom routes. No additional API
formats, authentication flows or provider capability discovery are introduced.
An incompatible execution catalog fails rather than changing model identity or
the stock JavaScript execution interface.

### Acceptance

Use synthetic definitions and fake credentials through actual launch consumers.
Cover unmodified model IDs, endpoint path/query/header fidelity, absence of
built-in discovery, command/env/anonymous auth, timeout/failure/refresh behavior,
HTTP/WebSocket prewarm and continuation, cancellation, unchanged catalog forwarding,
auxiliary capability decisions, credential isolation and incompatible-route
rejection. Installed Codex must execute a stock JavaScript tool once through the
custom route with normal captures and child completion. Verify fresh resume and
family inheritance as specified by Launch. No live-provider validation is implied
by deterministic fixtures or source review.
