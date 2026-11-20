# Terminal diff rendering

## Purpose and scope

Paint supplied source and diff rows with shared syntax, word emphasis, themes and
terminal-safe text. Captured-change ownership, provenance, navigation, selection
and preview lifetimes belong to the consuming feature.

Painting, tokenization and cache choices below are replaceable presentation design.

## Behavior

Decoration preserves source text, row identity and wrapping geometry. Deletions
and additions are tokenized separately. Partial or clipped unified output remains
displayable without claiming complete changed-line counts.

Layout records logical rows, wrapped rows, hunk boundaries and caller-supplied
source attribution together. Line numbers use separate old- and new-file
columns, so a number never changes meaning between rows; a file with only one
side, such as an added file, shows only that column. Deferred syntax decorates
intersecting complete hunks only; it cannot change that geometry. File-section
and viewport policy remain with the feature. Shared file labels use the model's
status, including rename sources and unavailable counts.

Replacement blocks emphasize changed words across consecutive rows while
preserving syntax colors. Retained words keep their ordinary row fill; pure
additions and removals do not invent word comparisons. Large blocks may omit
word emphasis to bound work. Graphemes, source text and copy coordinates remain
unchanged.

Unknown languages, oversized syntax inputs and lexer failures fall back to plain
text. Cancellation propagates. Each renderer retains bounded syntax caches;
sessions share no mutable cache. Wrapping breaks rows at word boundaries
where a word fits, otherwise at the row edge, and keeps every source byte on
some row. It carries styles onto independently painted continuation rows, and
row fills keep word emphasis out of padding.

Source text uses shared [sanitization](../textsanitize/SPEC.md). Theme detection
interprets supplied environment values and terminal replies without owning
terminal I/O.

Themes own the semantic foregrounds (success, warning, danger, standing work,
information, search, tool and hash) that terminal views share. Dark and light
themes use colors legible on their backgrounds. The terminal theme, used while
the background is unknown, defers to the terminal's own ANSI palette and keeps
syntax identifiers in its default foreground.

## Acceptance

Source, unified-diff, word-diff, cache, cancellation and theme tests
run inside the library. Rendered snapshots verify source styles; feature-owned
snapshots verify their composed headers, coordinates, viewports and previews.
