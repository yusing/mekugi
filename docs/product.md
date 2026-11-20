# Mekugi product

## Intent and authority

This document separates **binding user requirements** from **replaceable design approaches**:

- **User requirements**: outcomes, capabilities, and explicit scope constraints imposed by the
  user. Redesigns must preserve them unless the user changes them.
- **Current approach (replaceable)**: architecture, protocols, algorithms, defaults, bounds, and
  UI mechanics chosen by agents to satisfy those requirements. Being implemented or documented does
  not make a choice a requirement; redesigns may replace any of them while updating affected
  contracts and consumers together.

Owner specifications (`SPEC.md`) and boundary contracts (`CONTRACT.md`, `docs/contracts/`) follow
the same split.

## User requirements

### Required outcome

Deliver faster, cheaper agent work with fewer model round trips and higher quality than stock Codex,
within the explicit constraints below. Speed or cost gains alone do not establish success; claims
require controlled measurement (see [codex-setup-ab](https://github.com/yusing/codex-setup-ab)).

Rank tradeoffs within a change, while preserving correctness:

1. **Stock Codex cost**: reduce unnecessary round trips and payload size.
2. **AX / UX**: make agent and user workflows clearer, observable, and recoverable.
3. **Agent performance**: improve task effectiveness without weakening correctness or host authority.

### Explicit scope constraints

- **Codex remains the root execution authority.** Keep stock editing (`apply_patch`), command
  execution (`exec_command` via JavaScript `exec`), sandbox, permissions, process control, and
  batch/parallel semantics. Do not replace stock tools with custom editing languages or transports.
- **One interactive UI and one stock JavaScript execution interface.** Internal identifiers do not
  introduce extra user-facing modes. Direct execution mode is unsupported.
- **Product-local session resume.** Mekugi resumes Mekugi sessions; stock Codex resumes stock Codex
  sessions. Cross-product resume is unsupported in either direction.
- **Auxiliary features stay non-invasive.** Presentation, previews, metrics, and diagnostics do not
  alter execution outcomes or add display-only text to model context.

### Core user capabilities

- **Source-grounded continuity**: summary-free context resets that reload selected current sources,
  measured context-usage notices, and preloaded context for fresh delegates.
- **Bounded, recoverable context**: batchable bounded source reads, semantic/structural navigation,
  bounded command output with exact continuation, and lossless reference deduplication.
- **Exact change tracking and undo**: passive capture of stock patches and shell writes by short ID,
  focused cross-agent review, and surgical revert/reapply (including via `/rewind`) that preserves
  unrelated edits.
- **Observable, controllable sessions**: live edit previews, reasoning-grouped activity, interactive
  remote-write and `sudo` guards (even with `--yolo`), private secret dialogs (`sudo`/GPG) kept out
  of model context, and hook trust prompts.
- **Server-owned multi-client sessions**: terminal and browser UIs sharing a reusable Session API
  over an authenticated connector, surviving client disconnect, plus headless automation, custom
  Responses providers, offline inspection, and replay.

## Current approach (replaceable)

The current product implements these requirements through:

- **Native runtime (`runtime/codex`)** owning execution, V2 collaboration/waits, summary-free reset
  (`new_context`), source/skill acquisition, OS/Git environment facts, context-usage notices, and
  model-input output deduplication.
- **Go server and clients (`apps/mekugi`, `modules/`)** owning server-managed thread lifetimes,
  terminal and Svelte browser UIs, authenticated session helpers (`mcat`, `mrun`, `mread`,
  `msymbol`, `inspect_file`, `mchanges`), passive capture and 3-way merge revert/apply, shell
  guards, custom Responses routing, retained session history, and offline inspection/replay.
- **Single-generation model structure**: compaction selections and child reply summaries ride
  existing replies rather than adding separate summarization calls (except opt-in wait progress
  summaries for changed developers).
- **Minimal model-facing text**: guidance, tool descriptions, and results omit decoration.

User operation is documented in the [guides](../README.md#guides); contributor workflows live in
[Development](contributing/development.md); specs and contracts are listed in the
[documentation index](index.md).

## Non-goals

**Binding exclusions** (from scope constraints above):
- Replacing Codex's sandbox, permission flow, stock editing/execution tools, or root agent runtime.
- Direct execution mode or cross-product session resume with stock Codex.

**Current implementation exclusions** (not necessarily permanent):
- Relevance-filtered tool output, cross-session memory, interactive code editing, hosted
  benchmarking, binary file editing through source readers, or crash-atomic multi-file writes.
