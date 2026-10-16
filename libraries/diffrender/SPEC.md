# Terminal diff rendering

## Purpose and scope

Paint supplied source and diff rows with shared syntax, word emphasis, themes and
terminal-safe text. Captured-change ownership, provenance, navigation, selection
and preview lifetimes belong to the consuming feature.

## Behavior

Decoration preserves source text, row identity and wrapping geometry. Deletions
and additions are tokenized separately. Partial or clipped unified output remains
displayable without claiming complete changed-line counts.

Unknown languages, oversized syntax inputs and lexer failures fall back to plain
text. Cancellation propagates. Each renderer retains bounded syntax caches;
sessions share no mutable cache. Wrapping carries styles onto independently
painted continuation rows, and row fills keep word emphasis out of padding.

Terminal sanitization removes control sequences while preserving readable text.
Callers may explicitly retain valid SGR colors. Source input cannot emit cursor
movement, clipboard writes or terminal titles. Theme detection interprets supplied
environment values and terminal replies without owning terminal I/O.

## Acceptance

Source, unified-diff, word-diff, cache, cancellation, sanitization and theme tests
run inside the library. Rendered snapshots verify source styles; feature-owned
snapshots verify their composed headers, coordinates, viewports and previews.
