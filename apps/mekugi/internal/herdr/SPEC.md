# Native Herdr adapter

## Capability

Native Herdr surfaces share Mekugi captured review and agent navigation for the
current active Herdr space, including a space on another machine. Standalone
terminal presentation remains available.

## Current design

The adapter uses the existing same-user Session API server connection, shared
publications, metadata pages and content resources. It owns no listener, execution
runtime, retained store, Git change source or independent presentation view.

The Herdr consumer discovers the Mekugi terminal within the active space and runs
the helper on that space's machine. Space and machine identity scope observation,
selection and content reads; changing spaces retires the previous target's reads.
A remote failure never falls back to a local process with the same PID.

A native frontend associates its process ID and active attachment on its existing
RPC connection. The association follows completed client navigation and ends with
that connection. Lifetime and attachment identities reject process-ID reuse and
stale destinations. Each physical terminal retains its own association and drafts.

`mekugi herdr --pid PID --request JSON` performs one request and prints one JSON
object. Missing clients return `{"available":false}` successfully. Other failures
produce bounded diagnostics and a failing exit status. Clients can discover support
from ordinary command help before invoking the adapter.

Observation waits for changed shared state without recomposing retained history.
The helper bounds each request and output; large selected files use Session API
content continuation rather than a second composition or paging implementation.
Caller filtering keeps the shared saved-review baseline semantics. Live source
comes from existing provisional previews, with no filesystem reconstruction.

Selection captures the frontend, displayed thread and canonical roster name.
Server admission rejects stale or nonattachable targets, then retained native
effects invoke the terminal's ordinary display/reattachment path.

Viewer leases belong to the frontend, surviving active-thread changes without
affecting other terminals. Ordered native presentation effects carry absolute
expiry, so delayed delivery cannot restart a full lease or overwrite newer state.
Any active viewer suppresses duplicate terminal Diff, Agents, live-preview and
bottom navigation surfaces. Release or expiry restores ordinary geometry without persisting the
override or changing pane preferences. Standalone presentation remains unchanged.

A companion with the custom Files sidebar advertises file-opening capability on
its viewer lease. Clicked local file links then select that sidebar instead of a
terminal dialog. The producer resolves literal absolute paths and optional line
ranges, preferring an existing literal filename over a numeric suffix. The
consumer reads the file on the observed machine and navigates to the requested
lines; read failures belong in Files. Without an active capable viewer, the
ordinary dialog remains available. Nonregular paths retain clipboard behavior.

File selection is latest-wins, retained for repeat observation rather than queued.
Consumers deduplicate by physical frontend, activation, attachment and selection
ID. A replacement attachment does not inherit the previous attachment's selection.

A companion advertises edit-opening capability when its diff pane is available.
Clicked edit details reveal that pane and select the exact original diff in that pane, independently of its
cumulative Saved and Live views. Repeated clicks select again; subsequent review
updates do not replace the selected edit. Non-diff details and terminals without
an active capable companion retain their ordinary dialogs. Diff source stays on
the observed machine and is acquired through bounded content continuation.
