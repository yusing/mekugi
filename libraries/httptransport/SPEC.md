# HTTP transport primitives

## Purpose and scope

Share HTTP client defaults and response-body lifetime mechanics across provider
transports. Callers own credentials, endpoints, redirects, retry policy, and
request deadlines. The library owns no connection pool or application state.

## Behavior

Client construction preserves a supplied transport and client configuration.
Otherwise it clones the standard transport with a five-second dial timeout and
30-second keepalive. The supplied client is never modified.

Response wrappers preserve bytes and the original closer. Closing a
cancel-on-close body also cancels its request. An idle-read timeout measures
upstream read inactivity, excluding consumer backpressure. It closes the source
once and retains timeout, cancellation, and source-error identity together.
Prefix restoration makes bounded inspection transparent to the next reader.

## Acceptance

Provider and router consumers cover cancellation, idle reads, partial bytes,
backpressure, rejected responses, and exact byte replay. Package tests cover
client isolation and wrapper closure ownership without provider configuration.
