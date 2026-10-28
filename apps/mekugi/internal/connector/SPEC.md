# Connector

The connector admits remote human control requests and forwards the shared
Session API over the execution machine's private Unix socket. It owns no
execution, thread registry, transcript, upload files or browser assets. An
unavailable server produces a connection failure, never an empty session list.

Resource HTTP requests authenticate with an Authorization bearer header and use
separate upstream HTTP requests. Configured UI origins receive the corresponding
CORS preflight response; credentials are never accepted in cookies or URLs.
Every WebSocket connection authenticates with a bearer token in its bounded first
`initialize` message before any session data or control dispatch. The connector
creates one persistent random token in a private local file. An exclusive
lifetime lock prevents concurrent connectors from using the same token file;
independent instances need separate files. It never prints
the token or includes it in URLs, diagnostics or upstream messages. SIGHUP
rotates and privately publishes the token before admitting its replacement;
rotation cancels all requests and streams admitted with the old token. A failed
publication preserves the previous token and reports a content-free error.

Configured UI origins are checked before HTTP resource dispatch or WebSocket upgrade. Other origins are
refused, and origin checks never replace authentication. Non-browser clients
without Origin still require authentication. Initialization is bounded in bytes,
time and concurrent unauthenticated connections.
Cookies and query-string credentials are unsupported. Only the shared remote
human-control API is forwarded; local/native and agent-authority paths cannot
be reached through this listener. Caller headers cannot establish authority.

WebSocket listens on loopback by default. Operators terminate WSS at a reverse
proxy and protect the private connection from that proxy to the connector.
The browser connects directly to this endpoint; UI hosts receive no credentials.
After consuming the authentication message, the connector forwards the sanitized
initialization and relays JSON-RPC messages to the server-selected remote endpoint.
It never exposes the native endpoint. Subsequent messages retain their exact bytes;
mutations are never retried. Both relay halves close on rotation or shutdown. Upload
transport accommodates the shared API's decoded 32 MiB budget plus base64 and
message metadata, without maintaining a separate media store.
Remote WebSockets negotiate per-message compression without context takeover,
with uncompressed fallback. The private socket stays uncompressed. Message limits
apply to decompressed data, and compression changes neither token nor origin admission.

Closing the connector closes its own connections and leaves server-owned work
running. Each browser machine connection has an independent transport lifetime.
The Compose deployment borrows the execution machine's private socket directory
read-only and persists its own token directory separately. Its effective host UID
must match the server's user, preserving same-user socket admission.

Acceptance covers token persistence and permissions, failed/successful rotation,
active stream invalidation, origin and initialization behavior, version rejection,
exact post-authentication message forwarding, mutation uncertainty, unavailable servers and
independent server/connector lifetimes. TLS deployment is a separate check.
