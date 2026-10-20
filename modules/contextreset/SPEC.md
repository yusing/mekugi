# Inactive instruction loading

## Purpose

Retain bounded instruction-file loading for future context-reset work.

## Scope

REQ-CONTEXT-RESET-001: the module stays outside application composition. Normal
requests and manual or context-full compaction use the host/provider path, with
no local-reset option or recovery injection. Reactivation requires a separately
accepted integration.

## Behavior

The loader accepts decoded source observations and an absolute workspace. It
collects selected skills and instruction-named sources from completed reads in
the first five model responses, preserving retained source identity and ordering.
Previously restored guidance does not consume a model response. Global and
workspace-root instructions retain their contents; nested project sources use
structural outlines. The fileread and sourceoutline APIs own their respective
selection and outline semantics.

Skill content comes from an explicitly supplied acquisition function. The
application's existing skill reader owns native paths, manager delegation, and
its invocation environment. Collection does not select tasks or perform model
inference. Its output, including unavailable-source notices, is bounded to
12,000 tokens. An oversized source does not prevent a later source from fitting.

## Lifecycle

Collection is stateless and bounded by cancellation and a five-second deadline.
It starts no background service and registers no command.

## Codex connection

The codexbinding decoder interprets supplied Responses history, completed-call
identity, selected-skill envelopes, and restored-guidance provenance. It registers
no runtime binding. Ordinary compaction remains Codex-owned.

## Records

The module writes no records. Supplied ordering and skill identities describe
input provenance, not authorization to run or resume a turn. Recovery receipt
stores, synthetic completed-tool replay, task trees, summaries, and continuation
schedulers are outside this component. Existing user records remain untouched.

## Failure

Missing, unsafe, non-regular, non-UTF-8, oversized, or unreadable instruction
sources produce bounded notices rather than successful source identities.
Snapshot reads require link-safe platform support; unsupported platforms report
that limitation. A relative workspace admits no guidance.

## Acceptance

Module tests cover decoding and completion eligibility, source order, actual
reader selection and outlines, snapshot safety, and budget admission. Application
tests retain real skill acquisition and attachment-producer coverage. The module
has no production caller; installed-host compaction and guard coverage remain
separate from these offline component checks.
