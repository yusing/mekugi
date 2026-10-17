# Codex backend

## Purpose and ownership

Own Codex process transport, native-trace observations and rollout decoding. Codex remains the
execution, permission, agent, and session authority. The application chooses an
invocation, interprets lifecycle facts, and owns session presentation and intent.
The [Responses adapter](responses/SPEC.md) owns model-path adaptation; executable
frontend and input-projection registrations use the binding package.

## Execution authority

Codex owns stock `apply_patch`, `exec_command`, JavaScript `exec`, permissions,
sandboxing, processes, yielded sessions and `write_stdin` continuation. Mekugi
preserves nested tool names, schemas, arguments and host results. Observation,
presentation and replay never execute a call again, serialize independent
`Promise.allSettled` calls, or impose a minimum wait duration.

The application admits the JavaScript execution interface before inference;
catalogs exposing top-level `apply_patch` or `exec_command` instead are unsupported.
[Launch](../../apps/mekugi/internal/launch/SPEC.md#host-integration) owns the
invocation-local settings. The native shell hook is the narrow exception to
unchanged command text: composed guard and sh-tracking instrumentation can return
`PreToolUse.updatedInput`, preserving tool identity, other arguments, selected
shell, host results and process authority. Bash tracking uses shell startup.
[Shell instrumentation](../../libraries/shellinstrumentation/SPEC.md) owns the
shared rewrite and recognition rules.

[Output deduplication](../../modules/outputdeduplication/SPEC.md) changes only
eligible model-input text after original-output consumers finish. Separate
[change notices](../../docs/contracts/change-evidence.md#delivery-and-presentation)
require retained evidence. Neither replaces host output or grants execution authority.
[Executable frontends](../../apps/mekugi/internal/frontends/SPEC.md) and
[bounded commands](../../modules/boundedcommand/SPEC.md) run under the same host.

Native command approvals preserve the host's offered session, command-prefix and
permission scopes. The UI uses native decline for command denial, leaving the
original tool rejection intact and allowing the turn to continue. Optional denial
feedback joins only the matching thread, turn and tool's provider continuation;
it adds no synthetic exit code, steering or new turn. File-change and permission
choices remain unchanged. Passthrough preserves host decisions without typed
denial reasons. [Conversation](../../modules/conversation/SPEC.md#questions-and-approvals) owns
prompt lifecycle and draft preservation; the
[terminal session](../../apps/mekugi/internal/terminalsession/SPEC.md#questions-and-approvals) owns dock
interaction. Module guard denials remain separate shell-command failures.

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

## Transcript observations

The observation binding projects message and public-summary lifecycle facts into
backend-neutral values that distinguish item completion, text deltas and
summary-part boundaries. Values retain thread, turn and item identities and
the supplied observation time, without an RPC handle or execution authority.
Private reasoning is excluded. Original messages remain available unchanged for
control correlation, question/approval handling and diagnostics.

Live notifications, retained items and simulated playback share these values.
The consuming owner decides historical admission and presentation; projection does
not settle input, notify the desktop or issue host requests.

## History observations

The history binding decodes thread lineage, turns and item pages while preserving
host identity, status, failure and timing. Returned thread identities must match
the requested scope. Payload fields do not acquire presentation state or filesystem
authority. [Session presentation](../../apps/mekugi/internal/sessionpresentation/SPEC.md#history)
owns discovery bounds, page admission, placement and restoration completion;
session control owns request correlation and retirement.

Paginated history uses turn and item lists. Legacy children require full-history
reads because the item-list API does not support their rollouts. Errors never
trigger an unbounded fallback. Decoding neither resumes a thread nor restores a
live operation.

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

The observation binding decodes command starts, output and terminal outcomes,
preserving host identities, exact shell arguments and unknown exit status.
Application consumers share that decoding before presentation and during UI
delivery. [Command segments](../../modules/commandsegments/SPEC.md#live-correlation)
owns report matching; decoding neither correlates reports nor settles capture.

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

The binding decodes terminal or yielded execution results, preserving outer
success, known exit status, exact text and pending handles. Outer JavaScript success
cannot establish nested command success. These facts support live continuation,
capture, restored evidence and offline queries.
Retained carrier facts can inform classification after the consumer has validated
their identity; classification itself authenticates neither a record nor a result.
Opaque source and printed lookalikes cannot supply native-result provenance.

Continuation annotations have one wire definition shared by their producer and
recognizer. Recognition requires the exact annotation envelope and expected host
handle, so unrelated text remains intact. The application chooses advice from the
active tool catalog and tracks request-local provenance; the backend decoder never
resumes a process or chooses a next call. Offline findings remain query policy.

The backend's bounded, incremental native-trace reader observes only the private
directory selected for the invocation. It correlates executing thread, outer call
and source, `exec` cell, and individual native tool results. Consumers receive
decoded call and completion facts, including original command timeout metadata;
private trace schemas and incremental state stay in the backend. Printed results
cannot substitute for this provenance. Missing, ambiguous, unsupported or corrupt
trace evidence cannot confirm success. Shell success requires terminal exit zero;
completed dispatch and a yielded process remain unfinished. Application consumers
retain capture, pending-call and Activity policy. The
[change-evidence contract](../../docs/contracts/change-evidence.md) owns durable
receipts and the treatment of historical observation-only captures.

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

The offline-rollout binding attaches the bounded decoder to its consumer during
composition. It acquires no live backend resources. A scripted binder supplies
the same decoded-source interface for module acceptance.

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

Discovery can read only a supplied file's first metadata record using the same
file and line bounds. This identifies a candidate; it does not validate the
remaining rollout or certify that discovery found every matching source.

The replay consumer receives decoded item and turn observations separately from
logical-call inspection. Replay files are regular JSONL at most 64 MiB, with
records below 16 MiB. Known items require identity, turn and valid epoch-millisecond
start/completion endpoints; malformed records report their source and line.
Unknown variants remain coverage counts. Copied ancestor metadata, item ownership
and thread-settings boundaries determine inherited turn ownership. Private/raw
reasoning is omitted; public summaries and opaque user-content blocks retain their
meaning. Decoding supplies no synthetic events, wall clock or rendering policy.

An optional inspection observer receives decoded command and task/turn events. Without it,
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
