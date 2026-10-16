# Terminal diff rendering

## Purpose and scope

Paint supplied source and diff rows with shared syntax, word emphasis, themes and
terminal-safe text. Captured-change ownership, provenance, navigation, selection
and preview lifetimes belong to the consuming feature.

## Behavior

Decoration preserves source text, row identity and wrapping geometry. Deletions
and additions are tokenized separately. Partial or clipped unified output remains
displayable without claiming complete changed-line counts.

Layout records logical rows, wrapped rows, hunk boundaries and caller-supplied
source attribution together. Deferred syntax decorates intersecting complete
hunks only; it cannot change that geometry. File-section and viewport policy
remain with the feature. Shared file labels use the model's status, including
rename sources and unavailable counts.

Replacement blocks emphasize changed words across consecutive rows while
preserving syntax colors. Retained words keep their ordinary row fill; pure
additions and removals do not invent word comparisons. Large blocks may omit
word emphasis to bound work. Graphemes, source text and copy coordinates remain
unchanged.

Unknown languages, oversized syntax inputs and lexer failures fall back to plain
text. Cancellation propagates. Each renderer retains bounded syntax caches;
sessions share no mutable cache. Wrapping carries styles onto independently
painted continuation rows, and row fills keep word emphasis out of padding.

Source text uses shared [sanitization](../textsanitize/SPEC.md). Theme detection
interprets supplied environment values and terminal replies without owning
terminal I/O.

## Acceptance

Source, unified-diff, word-diff, cache, cancellation and theme tests
run inside the library. Rendered snapshots verify source styles; feature-owned
snapshots verify their composed headers, coordinates, viewports and previews.
