# Output recovery

## Purpose

Publish and page retained output through `mread`, without repeating its producer.

## Scope

Omitted stdout/stderr, source-row provenance, and retained evidence selections.
The frontend host owns authentication, selected session authority, leases, and
stream delivery. Evidence owners retain their selection schemas and rendering.

## Behavior

### Managed read continuation

Bounded command output, `mcat`, symbol references, and change reviews use the
authenticated `mread` executable frontend for retained continuations:
`mread REF [REF ...] [--stdout|--stderr] [--max-tokens N]`. An incomplete result supplies
the exact `read: incomplete; next_call: mread REF` command. There is no separate
cursor flag or caller-composed offset. References use short lowercase word
handles, such as `maple`, with a decimal suffix when needed. Handles are
feature-scoped locators, not secrets; full snapshot fingerprints remain internal.

Read and recovery handles allocate within one durable session namespace shared
by the root thread and its subagents. Unrelated sessions restart the sequence.
Forks and side threads snapshot the source's handles and allocation position
once, then allocate independently without changing inherited references.
Routing keys, model switches, request truncation, compaction, and router restart
do not change the namespace. Equal handles in different sessions cannot address
each other's records.

An initial reference owns only omitted output or a descriptor of existing
durable evidence, never an executable script. A subsequent reference stores the
original reference, two stream positions, the stream selection, and the original
record fingerprint without duplicating output. Repeated reads produce identical
pages and reuse next references while retained. Expired handles are not
reassigned or revived. A continuation inherits its stream selection; selecting a
different stream starts from the initial reference. Budgets may change.

Empty streams have no frame. Source reads report `[rows START:END]`; multiple handles
have per-handle labels, share one total budget, and produce one combined next call.
A failed handle writes a handle-qualified error on stderr and makes the invocation
nonzero without discarding successful pages or preventing other handles from being read.
Only valid unfinished handles appear in the combined next call.
Generated producer-limit diagnostics are not stored as omitted source stderr; actual
captured command stderr remains evidence and is not removed by text matching.
A page containing only generic stdout is unframed. Stderr
is always framed, and a page containing both streams frames both. Frames use
`[stdout UNIT]` or `[stderr UNIT]` and matching closing markers, where `UNIT` is
`bytes`, `rows`, or `json`. Framing is not payload. A rows page never cuts a
complete LF-framed row; a JSON page is a valid array of complete entries. A unit
that cannot fit reports its estimated token requirement and an exact retry command, without
a stack trace or nonadvancing reference. Units above the accepted maximum instead name
byte-oriented recovery. Malformed path/range operands explain that REF must be a returned handle.
The token budget includes frames, defaults to 8,000, and accepts 1 through 15,500. Page
completion returns status 0; an incomplete page returns status 1 with the next
call on stderr. The producer's status is preserved independently.

The authenticated executor persists records through the managed replay-store
locking and atomic write/rename path before exposing references. There are no
standalone output dumps. Omitted data is bounded to 16 MiB, encoded records to
the replay record limit, and all read records to a separate 256 MiB quota.
Storage failures are explicit. Cleanup follows the session-retention policy in
[REQ-ROUTER-001](../../doc/spec/router.md) and does not reclaim active dependencies.

Acceptance: the session basename `mread` resolves through the authenticated
pinned frontend and stock executor, preserves stdout, stderr, and status, and
does not route through a private command dispatcher. The worker binds
`CODEX_THREAD_ID` before reading, so unrelated sessions cannot use equal handles
while inherited fork and side-thread ownership survives restart and cleanup.
Visible combined next calls retain every referenced output and its dependencies,
so adopting one command does not lose its later handles when the parent expires.

## Lifecycle

Stateless with durable records. Composition supplies the retained history owner
and collected reference resolvers. Calls receive the authenticated retained
namespace and thread explicitly; a directory or textual reference alone grants
no cross-session authority. Recovery restores captured facts, never processes.

## Codex connection

One required executable frontend. Command execution uses the constructed module
service and the host's selected scope, exact argv, stdin, and cancellation.

## Records

The module owns output records, continuation cursors, their integrity bindings,
and dependency traversal. All publication uses `sessionhistory`. Already-issued
references retain their names, canonical record bytes, hashes, stream positions,
and inherited namespace ownership. Normal recovery rejects unknown record
members; historical adoption preserves its existing decoding contract.

A reference resolver contributes selection canonicalization and validation,
retained rendering, and dependency enumeration. The change-evidence owner supplies
both frozen selections and historical nonfrozen selections. Missing resolvers
report unavailable recovery rather than inventing content. Dependency inspection
keeps inaccessible retained evidence conservative. Cross-owner operations reuse
the active publication transaction; resolvers never open nested transactions.

The frontend reserves identities for reader delivery plans and publishes every
prepared payload before exposing their continuation. Publication failure may
leave conservatively retained records but cannot expose missing recovery.

## Failure

Independent bad references preserve other successful pages and qualified errors.
Cancellation stops delivery. Source content, cursor integrity, and unavailable
retained dependencies are validated without rerunning producers. Partial
publication never claims atomic multi-file or power-loss durability.

## Acceptance

- Registered execution and paging run with temporary retained storage and a
  scripted Codex binder, without a network or another feature's implementation.
- Fixed compatibility records preserve canonical bytes, cursor hashes, and
  already-issued reference lookup, including reordered selection input.
- Restart, changed/deleted sources, stream selection, multiple references, and
  exact token admission preserve retained content and provenance.
- Corruption and publication failures expose no unusable continuation.
- Fork and side-thread inheritance survives parent expiry without changing equal
  handles in unrelated namespaces or reviving expired references.
- Resolver absence, cursor cycles, and interrupted publication retain dependencies
  conservatively. Cross-owner callbacks operate within one publication boundary.
