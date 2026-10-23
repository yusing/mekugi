# Web UI contract

The UI host exposes only GET/HEAD page and embedded asset requests. Machine APIs,
authentication and recorded playback belong to the execution machine's connector
and [Session API](../sessionapi/CONTRACT.md), never to the UI host.

Without built assets, page requests receive 503 naming `make webui`. The index is
revalidated; hashed assets may be cached indefinitely. Responses forbid framing,
plugins and base-URL changes, restrict scripts/styles to bundled assets (plus inline
styles), and allow connections to explicitly selected WS(S) connectors. Markdown
images remain links rather than loading untrusted remote resources.

Browser control uses the shared JSON-RPC WebSocket protocol and generated remote
intent types. Credentials are sent only in initialization on the selected connector.
Attachment-bearing submissions use a separately authenticated connection so large
messages cannot block control; originating view, target and version stay captured.
Connection identities, not just logical thread IDs, scope state and routing.
Credentials are memory-only and are cleared on connection removal or page reload.

Publications and semantic view types mirror the shared API. Successful mutation
responses acknowledge admission; subsequent events provide execution outcomes.
Transport loss after dispatch leaves an unknown outcome and is never retried
automatically. Reconnected subscriptions acquire new snapshots and view identities.

Server-directed reattachment acquires a destination snapshot before changing the
selected route. It adopts that subscription only after source requests, input
preparation, effect receipts and retained recovery settle. Failed acquisition
releases only the candidate; explicit navigation releases both views. An
in-place server thread switch keeps its existing control subscription and updates
the request destination without attaching again.
