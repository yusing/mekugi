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
Native server and client queues replace superseded complete-frame updates so restoration
and ordinary progress do not accumulate copies of the same history. Attachment
transitions, acquired output and runtime exit remain ordered, and unacknowledged
effects remain available in the newer frame.
The [contract](CONTRACT.md) defines payloads. Consumers own rendering, drafts,
selection and request-result presentation, never execution lifetime.

Native startup binds its initial presentation stream in the admitting request, so
initialization failures cannot disappear between admission and attachment. The
runtime's joined exit result terminates native streams; transport loss alone is
never reported as a successful runtime exit. Startup operation identities last
only for their running owner, without retained completed-operation state.
