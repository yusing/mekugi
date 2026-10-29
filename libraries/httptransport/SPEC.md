# HTTP transport primitives

## Purpose and scope

Share HTTP client defaults and body lifetime mechanics across provider
transports. Callers own credentials, endpoints, redirects, retry policy, and
request deadlines. The library owns no connection pool or application state.

## Behavior

Client construction preserves a supplied transport and client configuration.
Otherwise it clones the standard transport with a five-second dial timeout and
30-second keepalive. The supplied client is never modified.

Encoded request bodies remain replayable with their exact content length.
Callers keep those bytes immutable while the transport owns the body. Synchronous
observers may borrow unread bytes without copying; they retain only detached
facts after forwarding, not the borrowed content.

Response wrappers preserve bytes and the original closer. Closing a
cancel-on-close body also cancels its request. An idle-read timeout measures
upstream read inactivity, excluding consumer backpressure. It closes the source
once and retains timeout, cancellation, and source-error identity together.
Prefix restoration makes bounded inspection transparent to the next reader.

A final-response exchange links request startup cancellation until an accepted
response is available. Accepted body reads then follow execution cancellation,
not the expired startup deadline. The sender owns any authentication retry and
returns its final response. Final rejection reads remain startup-cancelable,
are bounded to 8 KiB and five seconds, and close the response. Unreadable or
oversized details have an explicit explanation. Rejection status and bytes remain
available to the caller's error policy; this library does not format provider
notices or classify application diagnostics.

## Acceptance

Provider and application consumers cover cancellation, idle reads, partial bytes,
backpressure, rejected responses, and exact byte replay. Package tests cover
client isolation and wrapper closure ownership without provider configuration.
