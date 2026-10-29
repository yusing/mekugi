# Web UI contract

The UI host exposes only GET/HEAD page and embedded asset requests. Machine APIs,
authentication and recorded playback belong to the execution machine's connector
and [Session API](../sessionapi/CONTRACT.md), never to the UI host.

Without built assets, page requests receive 503 naming `make webui`. The index and
install manifest are revalidated; hashed assets may be cached indefinitely.
Responses forbid framing, plugins and base-URL changes, allow the bundled install
manifest, restrict scripts/styles to bundled assets (plus inline styles), and
allow connections to explicitly selected WS(S) connectors. Markdown
images remain links rather than loading untrusted remote resources. Bound user
images load through the authenticated Session API and display as browser-owned
blob URLs.

Browser control uses the shared JSON-RPC WebSocket protocol and generated remote
intent types. Control credentials are sent in initialization on the selected connector.
Each machine retains one control connection and lazily opens one reusable,
separately authenticated submission connection for attachment-bearing mutations.
Complete content and images use concurrent HTTP resource requests authenticated
with bearer headers on that connector. Image responses become browser-owned blob
URLs. Large resources never share a WebSocket writer; originating view, target and
version stay captured. Canceling a resource read leaves other reads available.
A lost submission connection is replaced on the next request, without replaying
failed mutations.
Connection identities, not just logical thread IDs, scope state and routing.
The browser retains machine identities, labels, connector URLs and credentials in
origin-scoped local storage. Reload restores connections with those identities;
token replacement and machine removal update only the affected saved record, so
changes in another open page do not overwrite unrelated machines or credentials. Runtime requests,
subscriptions and session snapshots are acquired afresh, never restored from storage.
Unreadable or invalid saved data produces a visible warning; valid records still
restore. Failed writes leave current-page connections usable and report that saved
machines or tokens may be stale, including inside machine settings. A successful
write for another machine does not clear an outstanding failure. Credentials are
cleared on connection removal. Removal closes both connections and cancels active
resource reads; replacing a token retires both, cancels active resource reads and
authenticates subsequent requests with the new credential.

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
