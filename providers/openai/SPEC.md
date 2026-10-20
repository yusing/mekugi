# OpenAI provider transport

## Purpose and scope

Own OpenAI credential validation, opaque model-catalog exchange, HTTP inference,
and dedicated upstream WebSocket connection establishment.
Codex owns login and refresh. The caller supplies prepared request bytes and
headers; the [Codex adapter](../../backends/codex/responses/SPEC.md) owns its
header selection and request metadata. The provider owns no agent, transcript,
conversation history, model selection, or downstream renderer.

## HTTP exchange and lifetime

Credential validation requires exactly one valid bearer Authorization header
and one nonempty account header; the application validates before preparing
provider requests. Catalog responses retain upstream bytes, status,
headers, and query parameters. Codex owns catalog caching and refresh.

Each HTTP inference attempt is forwarded once over HTTP, preserving the response
status, body and retry headers, including redirect responses. Redirect following
and retry policy remain with Codex, along with transport fallback.
Start cancellation covers connection setup and response startup; body consumption
retains the response lifetime and idle-read policy after startup completes.

Dedicated WebSocket connections belong to their caller. Rejected upgrades retain
the original status and response body. Dedicated connections and auxiliary HTTP
exchanges do not retry or follow redirects. The backend selects permitted auxiliary
operations and prepares their headers. Neither a failed write nor disconnection
authorizes replay.

Optional observations receive exact payloads at the receive boundary, before
consumer delivery. Closing an incomplete exchange settles already-received
observations before finalization. HTTP observation uses the supplied transport.
Missing observations do not prevent inference.

## Prediction configuration

The provider uses the invocation's latest authenticated request snapshot to fetch
composer prediction configuration and the desktop moderation policy. Credentials
remain in memory and account changes invalidate cached configuration. These bounded
auxiliary exchanges use the existing HTTP client, without redirects or retries;
they neither generate a suggestion nor provide an inference fallback. The remote
configuration owns eligibility, sampling, prompt and reasoning effort. The desktop
Statsig bootstrap supplies only the prediction moderation policy to this consumer;
unrelated assignments and telemetry are not adopted. Missing moderation policy
has the desktop's no-additional-suppression behavior.

## Acceptance

HTTP requests remain HTTP and each host attempt reaches the provider once,
including capacity rejection and long Retry-After advice. Cancellation, body
lifetime, status and headers retain their existing semantics. Dedicated socket
checks cover upgrade failures, native steering and interruption separately.
