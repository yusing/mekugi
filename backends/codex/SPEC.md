# Codex backend

## Purpose and ownership

Own Codex process transport and host-selected rollout evidence. Codex remains the
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

## Input projection

Bindings register named two-phase projections in composition order. Preparation
receives the original input before application additions; application receives the
final model-input copy after evidence observation. Registrations contain no
request-local state. Each preparation returns its own application function.
Duplicate names and incomplete registrations fail activation.

Projection results carry input bytes and repetition receipts for the application
consumer, without exposing backend execution or private request objects. The
binding API also owns recognition of Codex execution headers and live-session
JSON, shared by projection and continuation observation. These recognizers inspect
host metadata; they never interpret printed body text as a new host outcome.

## Retained rollout evidence

The rollout reader accepts an absolute path supplied for an identified host
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

## Acceptance

Process tests cover large and partial frames, original RPC IDs, ordered bursts,
observation-before-queue delivery, paused-consumer close, EOF shutdown, and forced
cleanup. Rollout tests cover identity, complete-record and byte bounds, newest
settings, nullable defaults, and context evidence. Native session tests cover live
usage precedence, resume settings, restored Activity, headless consumption, and
command-tracking correlation. Installed-host checks remain distinct from offline
process fixtures and retained-record decoding.
