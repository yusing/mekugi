# Session preferences

## Purpose

Restore applied session settings and renderer preferences for the selected
workspace and thread without restoring execution state.

## Scope

The module owns preference identity, records, applied-setting evidence,
resume selection and coalesced persistence. The application supplies host facts
and explicit invocation overrides. Backend decoding and control requests remain
with their existing adapters; terminal layout values and their validation belong
to the terminal adapter.

Preferences do not authorize execution, create resumable host history or retain
transcripts, prompts, drafts, credentials, processes or approval grants.

## Behavior

### Applied settings and resume

A complete applied snapshot contains model, reasoning effort, service tier and
observation time. Empty effort or tier in a complete snapshot means the explicit
host default. Partial historical evidence distinguishes an unknown tier from an
explicit default. Start/resume results and matching settings notifications are
authoritative; queued requests, acknowledgements, failures and foreign-thread
updates cannot replace Main's saved settings.

Resume selects the retained snapshot and newer identity-validated host facts
before a host resume operation can record invocation defaults. Newer complete
settings replace older evidence; a newer turn context contributes model and
effort without erasing known tier evidence. Explicit invocation overrides apply
independently per field. Missing evidence leaves host defaults in effect, with
presentation of that absence owned by the application.

Nullable-reasoning recovery retains the exact authoritative evidence selected
before resume and a watermark for the resulting transient host checkpoint.
Fresh-process retry ignores that checkpoint while accepting genuinely newer
history. Confirmation clears recovery evidence. The application owns the
corrective host request, its admission gate and rejection behavior.

### Renderer preferences

Presentation payloads carry a renderer identity and remain opaque to the module.
The terminal adapter validates and applies its own layout schema; other renderers
cannot interpret or overwrite terminal preferences. Reads of another namespace
never retarget an attached writer. New threads and other workspaces do not borrow
settings or layout.

Layout changes coalesce over 250 milliseconds and flush on orderly exit or
cancellation or session replacement. Applied settings and recovery checkpoints flush immediately,
without waiting for layout debounce. Unchanged state is not rewritten. A failed
save is reported once rather than retried on every frame.

The renderer stages an owned copy of its opaque presentation separately from
settings and recovery changes. Application coordination admits client layout
updates before an immediate flush, retaining the latest admitted presentation
with the authoritative settings. Saving already-staged state requires no renderer.

## Lifecycle

The composed service depends on session history and creates no files during
construction. An attached preference owner holds one current write target and
its pending state. Reads remain effect-free. The application supplies observation
time, flush points and presentation of failures. Closing an attachment restores
no host operation or live resource.

## Codex connection

The current application adapter supplies authoritative settings and decoded
historical evidence. This module issues no backend request. Host field names,
nullable-setting protocol details, acknowledgement/completion reconciliation and
input admission remain at that adapter boundary.

## Records

Preferences retain their existing workspace/thread hash and separate state
directory under `$XDG_STATE_HOME/mekugi/ui` (default `~/.local/state/mekugi/ui`). Version-1 records remain readable and retain
their flat JSON schema. That historical format identifies its opaque presentation
fields as terminal preferences; JSON member order carries no meaning.

Records are private and atomically replaced. Publication preserves last-completed
write behavior for simultaneous clients and does not enroll preferences in replay
quotas, catalogs, leases or age-based deletion. Reads admit only bounded regular
files up to 4 KiB; a named pipe cannot wait for a writer. Missing records use
defaults. Directory links retain their existing behavior; replacing a final
record link replaces the link, not its referent.

## Failure

Invalid, unavailable or incomplete records produce a presentation notice without
failing host resume or changing an applied setting. Reads never submit input.
Publication failure cannot claim that a setting or recovery checkpoint was saved.
Unsupported record versions and renderer identities remain explicit errors.

## Acceptance

Acceptance covers authoritative versus pending/failed/foreign updates, fresh
process restoration, independent invocation overrides, timestamp precedence,
nullable recovery, workspace/thread isolation and reads that preserve the active
writer. Renderer payloads retain version-1 compatibility and their own validation.
Real-file checks cover privacy, bounded admission, atomic replacement, no-op
writes, debounce, exit flush and failure reporting. Existing terminal and host
consumers retain their separate layout and live-protocol acceptance.
