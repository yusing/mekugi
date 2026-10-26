# Session API

The Session API owns detached publications, versioned JSON-RPC/WebSocket control,
and bounded snapshot/patch subscriptions. Launch composes execution owners and
applies requests on their existing Session Control event loop. The API owns no
provider implementation or second host request ledger.

## Server lifetime

The server registers in-process runtimes by their confirmed logical thread IDs,
never PIDs. Requests capture the selected thread and absolute workspace;
admission rechecks that identity and current prompt/operation state. The server
owns runtime contexts independently of client connections. Ending a thread or stopping
the server cancels and joins its runtime. Disconnected viewers release only their
subscriptions. Restart restores no active runtime or pending request implicitly.

All runtime creation uses one admission and lifetime owner. Human startup,
native startup and coordinated startup supply their execution inputs to that
owner; role and logical ancestry do not select a different connection or event
loop. Frozen coordinated preparation and attributed input retain their existing
authority checks.

Main and delegates use the same session attachments, operations and complete
runtime publications, including retained review. Opening a detail belonging to
another live runtime selects that runtime before opening its ordinary detail view.
Closing an attachment releases the view without stopping execution. A declared
role appears in the runtime's composer caption. Confirmed workspace and settings supply its path
base and caption, with no parent defaults for missing facts. Family review
composes admitted immutable captures once; roster totals and selected-child Diff
retain the same source evidence and authorship.

Each runtime publishes its own thread state. Family navigation and aggregate
changes are composed from those ordinary publications and admitted coordination
metadata at the server, independently of any selected or connected parent view.
Selecting Main, a sibling or a delegate uses the same thread-ID-based client
lifecycle. Per-thread drafts, prompts, pending effects and confirmed workspace
remain owned by that thread.
Logical family identity survives another runtime's thread replacement. Existing
members remain in their original family; an unavailable original Main supplies
no replacement transcript, settings or change evidence.

Creation and resume use the existing startup policy and retained history. Model
and workspace choices describe the execution machine. Acknowledgement means
admission; host outcomes arrive through publications. Ambiguous transport failure
never causes automatic mutation replay. Concurrent requests retain ordinary
admission refusals.

## Local transport

The control endpoint is WebSocket over a private Unix socket in the user cache under
`mekugi/server/control.sock`. Both peers require kernel-confirmed same-user
identity. The directory and socket are private; a lifetime lock prevents competing
listeners, and recovery removes only an unowned stale socket. Platforms without
peer checks refuse admission. The shared protocol version must match at readiness, upgrade and initialization
before control dispatch. Startup coordination
checks readiness, acquires an adjacent `.startup.lock`, rechecks, launches once
and waits within the caller context. Cancellation stops that wait without
killing a server already started; incompatible or ambiguous outcomes are not
automatically relaunched.

The connector supplies first-message bearer authentication and origin admission
for remote human connections. Those credentials never authenticate local agent identity or a
native secret-answer path. Session API projections omit secret content.

Subscribers receive a snapshot followed by patches. Queue pressure replaces pending
patches with an authoritative snapshot. Reconnection obtains a fresh snapshot
without replaying effects.
Live shared publications include revisioned saved review and current provisional
edit previews from the existing owners. Saved review uses the terminal Diff
pane's temporary-file policy and composed evidence; unchanged revisions reuse
their projection and encoding. Published user and View images share bound-image
read admission, including restored history.
Native server and client queues fold superseded owner updates so restoration
and ordinary progress do not accumulate copies of the same history. Attachment
transitions, acquired output and runtime exit remain ordered, and unacknowledged
effects remain available in the newer frame.
Native metadata updates retain unchanged immutable Activity and revisioned saved-review
owners across encoding, transport and client decoding. Complete replacements are
sent when an owner changes; snapshots reset retained identity. Other frame facts still participate
in change detection independently of the presentation revision; output and semantic
consequence budgets remain unchanged.
The [contract](CONTRACT.md) defines payloads. Consumers own rendering, drafts,
selection and request-result presentation, never execution lifetime.

Native startup binds its initial presentation stream in the admitting request, so
initialization failures cannot disappear between admission and attachment. The
runtime's joined exit result terminates native streams; transport loss alone is
never reported as a successful runtime exit. Startup operation identities last
only for their running owner, without retained completed-operation state.
