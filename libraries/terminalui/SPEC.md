# Shared terminal presentation

## Purpose and scope

Provide terminal input, raw-mode lifetime, Markdown painting and source-aware copy
fragments without feature state or backend control. Consumers own pane composition,
key bindings, selection lifetime, file-link authorization and backend interaction.

## Behavior

Raw-mode cleanup restores the caller's terminal state. Canceling an input reader
preserves caller descriptors and their shared file-status flags. Mouse reports
decode into input facts; they do not select a pane or execute a command.

Faint-support detection reports terminal capability. Consumers choose fallback
colors and meaning. Markdown shares bounded syntax decoration with `diffrender`
and lexical source boundaries with `sourceanalysis`.
Headings, quotes, code and responsive tables retain their source geometry through
wrapping. Subdued rendering preserves paragraph indentation and omits heading
colors and rules. File-shaped destinations become clickable only when the caller
authorizes them; ordinary web links need no filesystem authority.

Copy fragments reconstruct selected source, excluding generated gutters and fills.
They preserve code tabs, trailing whitespace, blank lines and hard breaks, balance
selected inline formatting, and rejoin soft wraps. Table cells retain their column
and quote context independently of narrow-screen layout. Fragment identities are
scoped by the consumer, and frozen selections need no mutable global registry.
Consumers extract private annotations before delivering rows to a terminal.

Mermaid drawing accepts a bounded supported flowchart subset,
respects available width and returns unavailable for unsupported syntax rather
than emitting a partial diagram. The text consumer owns fallback presentation.

## Acceptance

PTY tests cover cancellation, descriptor lifetime and terminal restoration.
Input tests cover mouse decoding and scrolling. Mermaid fixtures cover supported
directions, labels, crossings, bounds and unsupported input. Actual-renderer
snapshots cover Markdown styles, wrapping, links and responsive tables; selection
tests reconstruct their source. Native-session and offline-replay acceptance cover
composed frames and consumer-owned selection lifetimes.
