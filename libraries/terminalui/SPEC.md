# Shared terminal presentation

## Purpose and scope

Provide terminal input, raw-mode lifetime, Markdown painting and source-aware copy
fragments without feature state or backend control. Consumers own pane composition,
key bindings, selection lifetime, file-link authorization and backend interaction.

Choice lists measure columns from visible rows. Shared list and dialog drawing
preserves supplied syntax and source-copy annotations while sharing wrapping,
selection styling and hit geometry.
Consumers supply content, headers and actions; they own navigation and effect
dispatch. Rendering neither reads files nor opens links.

Auxiliary panes share painting, decoded input, theme and attachment-reset
contracts, with feature-owned titles and contextual hints. Source selection retains complete documents, attribution and pinned
headings. Navigator preferences and editor targets are optional capabilities.
Typed attachment bridges supply detached publications before painting; panes
receive no source service, store or backend handle. Typed roster declarations
select a feature-owned painter without introducing feature dependencies here.

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

Canonical agent names and colors are shared by activity and Diff presentation.
The root displays as `main`; children display their path below `/root/`. Colors
depend only on canonical identity and survive reconnects. Shared path decoration
emphasizes the filename without changing the supplied display path. Callers still
resolve workspace-relative display and authority before painting.

## Authored Markdown

Inline code uses content-detected syntax colors, with the existing accent for
plain or unrecognized spans; fenced code uses its language. Styling never changes
source, wrapping or copying. Recognized local links show their label as a terminal
hyperlink; wrapping preserves the destination without underlining padding/gutters.
Raw file-shaped text and inline code use caller-admitted destinations without
filesystem probes. The [terminal consumer](../../apps/mekugi/internal/terminalsession/SPEC.md#content-dialogs)
owns local-file admission and opening. Spaces may be enclosed in code or an explicit
link. Fenced code stays literal.

ATX headings require one to six markers and a space or line end; closing markers
are hidden. Setext underlines make the previous paragraph an H1/H2. Headings are
bold, with theme magenta/cyan/yellow/green/blue for levels one through five and
default foreground for level six. Inline styles restore their heading style.
H1/H2 add dim double/single rules as wide as the title, at least three cells.
Subdued reasoning uses bold headings without colors/rules. Source copy preserves
markers and Setext underlines while excluding generated rules.

Tables require a complete matching delimiter row and support optional outer
pipes, escaped pipes and code spans. They paint bordered grids with emphasized
headers, alignment, links and wrapped cells. Fenced tables stay literal;
blockquote tables keep their rail. Narrow tables become separated header/value
records rather than clipping columns. Labels share a display-width column, or
stack above values when that column would exceed half the pane. Rendering follows
current streaming/restored text and width.

## Mermaid

Mermaid drawing accepts a bounded supported flowchart subset,
respects available width and returns unavailable for unsupported syntax rather
than emitting a partial diagram. The text consumer owns fallback presentation.
Supported forms include flowchart/graph in TD/TB/BT/LR/RL directions, rectangle,
decision and stadium nodes, solid/dashed directed/undirected/bidirectional edges,
pipe or spaced directed labels, and chained `&` endpoint groups. Quoted labels
preserve punctuation and semicolons; `<br>`, `<br/>` and `<br />` wrap node labels.
Horizontal layouts fall back to their vertical equivalent when necessary without
losing nodes or edges. Every edge has its own lane; crossings are not junctions.

Limits are 16 KiB source, 16 nodes, 24 expanded edges, 24 references per group,
256 display cells per node label, 40 per edge label and 65,536 canvas cells.
Unsupported syntax, other HTML/entities, unsafe/non-additive-width labels, open
fences or unfit diagrams preserve source display. Subgraphs and other Mermaid
families remain unsupported. Drawing uses no external renderer and follows text
and width, including quotes/replay. The port derives from
[Codex 0.159.0](https://github.com/openai/codex/tree/687a119f0fcaace47e1f1abcc77cec6c813fd6da/codex-rs/mermaid).

## Acceptance

PTY tests cover cancellation, descriptor lifetime and terminal restoration.
Input tests cover mouse decoding and scrolling. Mermaid fixtures cover supported
directions, labels, crossings, bounds and unsupported input. Actual-renderer
snapshots cover Markdown styles, wrapping, links, responsive tables and dialog
surfaces; selection tests reconstruct their source. Native-session and offline-replay acceptance cover
composed frames and consumer-owned selection lifetimes.
