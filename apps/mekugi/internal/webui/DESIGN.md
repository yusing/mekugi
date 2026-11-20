# Web UI design

Visual and interaction rules for the browser UI (`apps/mekugi/internal/webui/app`).
[SPEC.md](SPEC.md) owns required behavior; these rules are replaceable presentation design.

## Components and tokens

- Build from the locally owned shadcn-svelte primitives (`app/src/lib/ui/`) and Mekugi/AI Elements
  compositions; Session API publications and semantic controls remain the source of data and
  actions.
- Keep Nova shapes, neutral fills, quiet hovers, and 44px phone touch targets. Use spacing,
  typography, and surface tones for hierarchy instead of nested card frames; reserve borders for
  text inputs, floating overlays, and structural pane dividers.
- Reuse shared recipes in `ui/styles.ts`: callers add layout only and never override recipe
  properties. Add a recipe when two places share a new look.
- Use semantic tokens in `app/src/app.css` for both light and dark palettes; never hardcode hex
  values in components. Keep `ui-muted`/`ui-accent` distinct from muted text and violet accent ink,
  and give destructive hover fills readable ink.
- **Tailwind gotcha**: use literal class names only (never assemble classes with string operations).
- Page chrome uses icon buttons, chips, or segments (theme toggle cycles System/Light/Dark in
  Machines); native inputs belong in dialogs/forms wrapped in `Field`. Keep one glyph per meaning.

## Layout, space, and alignment

- Reserve no space for hidden content; conditional padding applies only when rendered.
- **Svelte `:empty` gotcha**: `:empty` fails on Svelte whitespace/comment nodes; hide empty rows
  with `not-has-[>:first-child>*]:hidden` or `{#if}`.
- Hover-revealed transcript actions stay in the right gutter (`rowAction`/`revealed`), taking no
  in-flow row; only failures take space beneath a row.
- Shared grid/subgrid columns span empty neighbors; elide leading path segments via `Path` while
  keeping full text selectable/copyable.
- Size toolbar breakpoints from measured widths (desktop tools fit one row with side panel open;
  phone keeps Stop/Send on the tools row). Center-align mixed-height wrapping flex rows.
- Scroll the region that grows (card bodies scroll under pinned headers; never scroll multiple cards
  together).
- Transcript rows use a 60px time gutter, 16px gap, content column, and action gutter; markers sit
  at the content edge without invented bullets/indents. Center controls ("Show earlier entries") on
  the content column. Keep secondary facts inline when they fit; give clickable rows horizontal
  padding so hover clears carets and trailing text.

## Type, focus, dialogs, and content

- Match sibling text size/weight in a row; keep composer pickers/Fast toggle visually consistent
  with new-thread pickers; give icon-only controls tooltips and accessible names. Render agent names
  through `AgentName` (`quiet` in secondary text) and empty states in muted ink.
- **Focus**: only interactive controls show focus rings. Never make containers focusable to park
  focus (temporary hand-off rows drop `tabindex` on blur; self-focused dialogs show no ring).
- Panel tabs are underlined text tabs (even when alone); segments are for in-content toggles.
  Dialogs use `Field`, quiet Cancel first, primary submit, `danger` for destructive confirms, and
  `Alert` (not warning `Notice`) for failures; phone dialogs are safe-area bottom sheets.
- Metrics dialogs use compact tabular-numeral tables with pinned view/exchange navigation, preserving
  zero values, units, and paths.
- Show reader-facing text, not raw wire diagnostics; only image paths get image preview boxes.

## Refresh, loading, and themes

- **Keep shown content until its replacement arrives**: refresh, reconnect, or navigation never
  flashes back to a skeleton, empty list, or Ended. Keep roster and family navigation mounted across
  thread navigation.
- Discard superseded reads silently; show only current failures. Key reads by source identity
  (attachment, revision, index), not wrapper objects.
- Loading/busy states take no in-flow row; preserve reading anchor and focus as content arrives.
- Every change must work in light and dark palettes with ≥4.5:1 text contrast (including syntax on
  tinted diff rows, checked by `TestWebUIBrowserDiffs`). CSS sets dark ground before script runs to
  prevent light flash.

## Rendered verification

Source review does not prove layout. Build with `make webui`, then run browser tests in
`apps/mekugi/internal/launch` with `MEKUGI_TEST_WEBUI_BROWSER=1` and `MEKUGI_TEST_WEBUI_ARTIFACTS=<dir>`
across affected viewports (desktop 1280px, tablet, phone 390px), light/dark themes, and realistic
long content. Measure geometry with `getBoundingClientRect`; verify delayed refreshes retain old
content; check uncovered surfaces (e.g. preset dialog) directly before calling them done.
