# Connector

## Requirements

- Expose the reusable Session API to remote clients (including browsers) so the UI host can run
  separately from execution machines.
- Authenticate with a persistent, explicitly rotatable bearer token; TLS termination belongs to an
  external reverse proxy.
- Closing the connector must leave shared server-owned threads and active work running.

## Current approach (replaceable)

- Forwards only the remote human-control subset of the
  [Session API](../sessionapi/CONTRACT.md) to the execution machine's private Unix socket; local
  native and agent-authority endpoints are unreachable through the connector.
- Generates and locks one persistent random token in a private file (never printed or put in URLs,
  cookies, or diagnostics). WebSocket connections authenticate in the bounded first `initialize`
  message; HTTP resource endpoints use `Authorization: Bearer`. Configured `--origin` values are
  enforced alongside (never instead of) token auth.
- `SIGHUP` atomically rotates the token and closes connections using the old credential; failed
  rotation preserves the previous token.
- Listens on loopback by default, negotiates per-message WS compression without context takeover
  (limits enforced on decompressed payloads, supporting the 32 MiB upload budget), and never
  retries mutations after transport loss.
