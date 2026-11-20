# Implementation

Locate the owner in `CONTEXT-MEKUGI.md`, read its spec, and load only crossed boundary contracts.
`docs/contracts/composition.md` owns workspace layout, descriptors, lifetimes, and import rules.
Separate user requirements from replaceable design choices per `docs/product.md#intent-and-authority`.
Update affected contracts and consumers together when changing a behavioral commitment.

## Architecture invariants

- **Extend owners, pass actual owners.** Keep each feature's behavior, records, views, and bindings
  together; extend an existing owner before adding a module. Pass narrow typed dependencies or
  admitted snapshots, never an application back-pointer, ambient accessor, or path to reconstruct
  a store. Keep one authoritative implementation and store; adapters do not duplicate policy or
  transcripts.
- **Explicit lifetimes and thread kinds.** Put thread/turn state in scoped stores; avoid package
  globals, `init` registration, and competing lifecycle maps. Keep feature codecs with their
  feature, retention with `sessionhistory`, and cross-owner publication with `recordtransactions`.
  Thread-scoped behavior must cover Main, delegates (running/idle/completed/resumed), Side/One-off
  forks, and rewound roots; never assume a live runtime, readable native history, or root identity.
- **Separate semantics from painting.** Module models own renderer-neutral state and intents;
  renderers own geometry, keys, selection, and painting. The browser is a renderer too: publish
  shared decisions (display names, disclosure, folding, tone, wait labels) via the Session API
  instead of re-deriving them in TypeScript. Measured geometry stays with the renderer.
- **No speculative contracts.** Add contracts or slots only for a present consumer.

## Presentation

Reuse stock/shared UI and format workspace paths with `libraries/pathdisplay.ForWorkspace`
(preserving external/operational paths). Settle genuinely new UI shapes with the user.

Displayed facts arrive via multiple paths (live/restored/paged/replayed, Main/child, pending/settled,
terminal/browser). Converge display fixes where sibling paths meet, treat examples as samples of
their input class, and check one representative per affected sibling path.

## Performance

Size recurring work and growing-data paths for retained history size, request size, and concurrent
threads, not a fresh short session. Use existing owner revisions and admitted snapshots to skip
unchanged work; keep small updates proportional where the contract allows, and check downstream
consumers so upstream reuse does not hide repeated copying, serialization, or tokenization.
Cover changed cost invariants with work-count, allocation, retained-growth, or scaling checks rather
than machine-specific wall-time limits.
