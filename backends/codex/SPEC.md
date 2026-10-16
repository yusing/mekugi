# Codex backend

## Purpose and ownership

Own Codex process transport and rollout decoding. Codex remains the
execution, permission, agent, and session authority. The application chooses an
invocation, interprets lifecycle facts, and owns session presentation and intent.
The [Responses adapter](responses/SPEC.md) owns model-path adaptation; executable
frontend and input-projection registrations use the binding package.

## Process transport and lifetime

The app-server client launches the supplied command and owns its stdio connection
and child-process cleanup. RPC frames are newline-delimited. A complete final
JSON frame without a newline is accepted; malformed or truncated JSON fails the
transport. Frames can contain a full resumed history and have no fixed byte-size
ceiling. A bounded pending-message queue applies backpressure while preserving
order, rather than dropping bursts or terminating a slow consumer.

Bounded observation callbacks run before presentation queuing. They observe the
original decoded message without mutating it or executing effects. Diagnostic
stderr remains separate from RPC and terminal painting. RPC replies preserve the
original server request ID; acknowledgement does not establish operation completion.

Explicit close releases a reader waiting on a paused consumer. Graceful shutdown
closes input and allows Codex three seconds to drain before forced termination;
the caller receives shutdown and process errors. A process and live RPC handle
are never restored from retained evidence.

## Native shell hooks

The backend owns the native PreToolUse schema, command-hook registration and
trust identity, supported-tool matching, and effective-hook validation. The
helper replaces event-supplied resource paths with trusted invocation bindings
before instrumentation.
Instrumentation returns updated command text without executing it; approval is
requested only when the selected shell reaches a guarded command.

The command adapter translates helper launch or process failure into a blocking
native hook result. Host hook-shell launch, timeout and delivery failures retain
Codex's external-hook policy. The application checks effective workspace hooks
before admitting each new user turn and preserves drafts on failure. Feature
approval policy and session grants remain separate from this protocol owner.
Hook matchers outside the verified shared regex syntax block guarded admission
with an explanation; an unverified matcher is never treated as nonmatching.

Guarded startup uses a bounded discovery process with the same invocation
configuration and absolute workspace. Host-resolved session-layer hook groups and
state are preserved, including empty groups and original handler indices. The
backend appends its guard and disables exact incompatible ordinary hook keys only
through invocation overrides. Managed conflicts remain unavailable. Discovery
starts no thread or inference, closes its process before final launch, and leaves
persistent settings unchanged. Per-turn validation checks the actual selected
thread workspace; later conflicts block admission rather than being rewritten.

## Shell approval registrations

Bindings declare remote-write or sudo participation and a module-owned grant
factory. Duplicate kinds and incomplete factories fail composition. Launch installs
only registered helper resources and the local decision server rejects unregistered
request kinds before presentation. With no guards, independent tracking remains
available and caller hooks are preserved. Native hook availability is still checked
against the selected host at launch and turn admission.

The backend owns the private command/askpass wire types and resource names.
Secrets travel only to the askpass pipe; they are absent from grant identities.
The application owns shared socket lifetime and UI delivery, while modules own
policy and exact grants. Removing a module does not change another module's grants.

## Input projection

Bindings register named two-phase projections in composition order. Preparation
receives the original input before application additions; application receives the
final model-input copy after evidence observation. Registrations contain no
request-local state. Each preparation returns its own application function.
Duplicate names and incomplete registrations fail activation.

Projection results carry input bytes and repetition receipts for the application
consumer, without exposing backend execution or private request objects.

## Execution evidence

The binding recognizes supported transparent JavaScript tool invocations for
preview, continuation correlation and offline inspection. It preserves literal
tool names and arguments without executing source. Unknown syntax, dynamic
arguments, shadowed runtime bindings and additional effects remain opaque.
Literal bindings and supported Promise batches can supply preview candidates;
continuation correlation additionally requires the original result metadata.
Output-only projections are therefore insufficient for that correlation.

These candidates establish neither execution nor success. Host observations and
retained filesystem evidence remain authoritative; UI labels and corpus findings
belong to their consumers. Recognition never rewrites the submitted program or
serializes parallel calls.

The binding also decodes execution handles, result text and host completion
headers for live continuation projection, restored evidence and offline queries.
Retained carrier facts can inform classification after the consumer has validated
their identity; classification itself authenticates neither a record nor a result.
Opaque source and printed lookalikes cannot supply native-result provenance.

Continuation annotations have one wire definition shared by their producer and
recognizer. Recognition requires the exact annotation envelope and expected host
handle, so unrelated text remains intact. The application chooses advice from the
active tool catalog and tracks request-local provenance; the backend decoder never
resumes a process or chooses a next call. Offline findings remain query policy.

## Guidance projection

One composed guidance registration provides three explicit request phases: before
preparation, at execution preparation, and after attachment matching. It receives
only instruction/input bytes, returns changed-field flags, and can reject malformed
owned carriers. The application applies changed input through its catalog-indexing
boundary; provider-prefix reconciliation remains later in the request pipeline.
Late selected-skill projection receives invocation-discovered manager availability.
The same registration assembles selected frontend descriptions and refreshes or
removes its marked tool-description section. Duplicate or incomplete registrations
fail activation. No registration means these effects are absent.

## Logical-thread naming observations

A composed naming factory supplies one typed policy callback bundle per accepted
Main thread scope. Application-owned scopes survive view switches until invocation
shutdown. The callbacks receive metadata, name changes, original model input, and
successful upstream-exchange completion. Atomic admission returns an operation;
the application runs it with the triggering exchange's bound inference adapter
and delivers its candidate with its original thread identity. Candidate policy is
rechecked before host persistence. No registration means no automatic naming.
Manual name-save control and host-title presentation remain application-owned.

## Retained rollout evidence

### Host-selected context and settings

The host-selected reader accepts an absolute path supplied for an identified host
thread. Only a regular file whose initial session metadata names that thread is
evidence. Named pipes are rejected without waiting for a writer. Metadata reads
are bounded to 64 KiB. Invalid, missing, or mismatched evidence remains unavailable.

Context-usage and resume-settings readers examine complete records in the last
8 MiB, ignoring a cut boundary fragment and an unfinished final record. Context
usage distinguishes absent evidence from a known zero. Resume settings select
the newest value for each supported field, preserve explicit nullable defaults,
and may exclude records at or before an application-supplied timestamp.

The application chooses whether restored facts may replace live state, applies
invocation/preference precedence, and gates resumed input on host confirmation.
It projects restored Activity with its own 64 MiB reading policy over the same
identity-validated opener. Rollout reads cannot resume a thread or replay effects.

### Supplied offline rollouts

Logical inspection reads the caller-selected regular UTF-8 JSONL file without
starting Codex, scanning other sessions or writing state. Files are at most
64 MiB, lines below 32 MiB and logical calls limited to 10,000. Malformed,
conflicting or oversized input fails before a complete source is returned.
Historical JSON decoding semantics remain compatible with retained rollouts.

Calls and output-only identities retain first-observed order, original line and
time, preceding workspace/model, tool identity and every recorded output.
Identical repeated calls coalesce; conflicting inputs, call workspaces or thread
identities fail. Missing call IDs are invalid. Unknown record kinds are ignored.
Workspace canonicalization, retained-record matching and query results belong to
the inspection consumer.

An optional observer receives decoded command and task/turn events. Without it,
event-specific payloads are not interpreted. Persisted execution endpoints take
precedence over envelope recording time; absent or null endpoints remain unknown.
The consumer owns metric identity validation, pairing and completeness. Observer
failure or cancellation stops the read without claiming a complete source,
including an empty source or cancellation while observing its final record.

## Acceptance

Process tests cover large and partial frames, original RPC IDs, ordered bursts,
observation-before-queue delivery, paused-consumer close, EOF shutdown, and forced
cleanup. Rollout tests cover identity, complete-record and byte bounds, newest
settings, nullable defaults, and context evidence. Supplied-rollout acceptance
covers logical call ordering, original metadata, conflicting identities, command
endpoints, optional event observation and source bounds. Query consumers cover
retained-record matching and whole-rollout AX reporting. Static-source tests cover
literal argument preservation, transparent result projection and rejection of
opaque or effectful wrappers; preview and continuation consumers retain their
separate acceptance. Native session tests cover live
usage precedence, resume settings, restored Activity, headless consumption, and
command-tracking correlation. Installed-host checks remain distinct from offline
process fixtures and retained-record decoding.
