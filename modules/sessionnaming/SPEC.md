# Session naming

## Purpose

Generate one optional name for a new empty Main thread while preserving manual
and host naming authority.

## Scope

Naming observes accepted Main start/resume metadata and the model-request path.
Children and side questions are ineligible. Existing names or nonempty history
suppress automatic generation. Host-title display and manual name-save
reconciliation remain application responsibilities.

## Behavior

Retain the first submitted user request text and its supplied file or directory
snapshots, without reopening files or including skill instructions, tools, or later
history. A failed exchange may establish that first prompt but cannot start naming.
The first successful completed upstream exchange atomically consumes the one
naming attempt. Prewarm, compaction, and unsuccessful responses do not admit it.

Naming uses `gpt-6-luna` with medium reasoning, at most 4,096 input runes, and a
one-minute operation deadline. Its instruction asks for a concise title in the
user's language. Successful text is whitespace-normalized. Missing provider access,
invalid credentials, unavailable Luna, rejected or unsuccessful responses, and
empty text silently leave the current name unchanged. There is no availability
probe, model fallback, or later retry. Skipping unavailable inference still consumes
the attempt.

Manual renaming suppresses automatic naming before persistence, even if that save
fails. Host name updates also take precedence. A generated candidate is rechecked
at delivery, so an already-running or queued result cannot override newer naming.
Automatic candidates are displayed only after application-owned host confirmation.

## Lifecycle

Each admitted inference is a host operation. The application owns logical thread
scope entries from accepted Main admission until invocation shutdown. Each entry
holds only that module's typed policy state through its callback bundle. The
module has no thread-ID map. Switching views or unsubscribing a thread retains its
eligibility history and outstanding operation identity; returning to that thread
reuses the entry. Shutdown cancels admitted work. Fresh resume derives eligibility
from host metadata and never revives an operation.

## Codex connection

The binding registers a per-thread naming factory. Application dispatch supplies
accepted metadata, original request input, name updates, and successful upstream
exchange completion at their existing sites. Completion is not inferred from a
turn-completed notification. The application's name-save path issues every
`thread/name/set` request and reconciles acknowledgement, ordering, and failure.

The inference adapter captures authentication from the successful triggering
exchange and attributes accounting to its originating thread. Authentication,
Responses encoding, transport, terminal validation, and usage remain adapter-owned.
Execution and steering identity never enter the auxiliary naming request. Naming
usage is included in the session's existing totals; skipped requests invent none,
and admitted exchanges with missing usage remain distinguishable from rejection.

## Records

None. The host retains confirmed thread names. Invocation-local suppression and
unfinished operations are not durable records.

## Failure

Inference failure is silent and cannot block or alter the original response.
Application name-save failure restores confirmed presentation and produces nonfatal
feedback without changing conversation work. Removing the module removes automatic
naming while host metadata presentation and manual rename control remain available.

## Acceptance

Module acceptance covers metadata eligibility, first-prompt retention, concurrent
one-attempt admission, cancellation, input bounds, normalization, and suppression
before and after inference. Binding acceptance decodes real submitted snapshots
without mutating input. Consuming tests cover isolated auth, actual JSON/SSE
responses, completed-exchange triggering, usage provenance, composition removal,
manual-save serialization, fresh resume, switch isolation, and unchanged title
rendering. Installed-host persistence is distinct from offline request fixtures.
