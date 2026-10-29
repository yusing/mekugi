# Web UI design

Visual and interaction rules for the browser UI. [SPEC.md](SPEC.md) owns behavior;
this file owns how it looks and responds.

## Use the kit

- Build from the locally owned shadcn-svelte components in `app/src/lib/ui/`
  and their Mekugi compositions. AI Elements compose these primitives for
  messages, reasoning, tool output and delegated work. Session publications and
  semantic controls remain the source of data and actions; examples' chat
  transports and lifecycle assumptions do not define session behavior.
  `app/components.json` selects the upstream Nova registry and local aliases;
  copied components are maintained here with the application palette. Buttons,
  picker and composer controls, menus, dialogs and chrome badges retain the
  Nova shapes with neutral fills and quiet hover states. Phone controls retain
  44px touch targets. Use spacing, typography and surface tones for hierarchy,
  rather than outlining every section. Welcome content and activity groups sit
  directly on the page. Tool summaries use compact Trace/Collapsible rows;
  expanded output keeps its code well without an enclosing card. Avoid nested
  card frames. Borders remain for text-entry affordances, floating overlays and
  structural pane boundaries where they clarify interaction or separation.
- Shared controls and the recipes in `ui/styles.ts` include:
  `Button`, `Field`, `Alert`, `Notice`, `Dialog`, `Menu`, `Picker`, `Popover`,
  `Tag`, `chip`, `segments` and `segment`, `input`, `floating`, `rowAction` and
  `revealed`, `codeBlock`, `emptyBox`. Callers add layout only, never a property the recipe sets. When two
  places need the same new look, add a recipe.
- Colours come from the semantic tokens in `app/src/app.css`. A missing role gets
  a token in both palettes; components never carry hex values.
  Stock muted and accent surfaces map to `ui-muted` and `ui-accent`, separately
  from the application's muted text and violet accent ink. Destructive controls
  use their own ink token to keep text readable on tinted hover fills.
- Tailwind sees only literal class names. Never build class names with string
  operations.
- Page chrome uses icon buttons, chips or segments, not native form controls. The
  theme choice is one quiet icon button in the Machines row that cycles System,
  Light and Dark. Native inputs belong in dialogs and forms, wrapped in `Field`.
- One glyph per meaning. Rename must not reuse New thread's compose icon.

## Space

- Reserve no space for content that is not shown. Padding for an optional
  element, such as the roster's context bar, applies only when it is drawn.
- A hidden row must leave no gap. `:empty` does not match Svelte output, which
  keeps whitespace and comment nodes. Test for element children
  (`not-has-[>:first-child>*]:hidden`) or wrap the row in `{#if}`.
- Hover-revealed actions take no in-flow row. A transcript row's actions stack in
  its right gutter (Details, then Copy) using `rowAction` and `revealed`. Only a
  failure message takes space under the row.
- Columns shared through a grid or subgrid size to their widest row. A row with
  nothing for a column spans its neighbour into it, so a path in an activity row
  wraps only where the row runs out.
- Toolbar rows prefer icon buttons. Choose container breakpoints from measured
  widths, not guesses: the composer's tools fit one row on desktop with the side
  panel open, and on phones Stop and Send stay on the tools row.
- In wrapping flex rows of mixed-height items (attachment chips), align items to
  the centre so one tall item does not stretch its neighbours.
- Scroll the region that grows. A card with a long body keeps its header in view
  and scrolls the body; an outer container never scrolls several cards' content
  together. A layout for several items fills the space when only one is shown.

## Alignment

- Transcript rows have a 60px time gutter and a 16px gap, then the content
  column, then the action gutter. Row markers sit at the content column's edge,
  as on hook and thought rows: an icon, then muted text, 26px tall. Do not
  invent bullets or indents.
- Centre a control on the column it belongs to. "Show earlier entries" centres on
  the content column, not across the time gutter.
- Clickable rows (summaries, list items) get horizontal padding so their hover
  fill clears the caret and trailing text.
- Secondary facts about a subject (agents, programs, counts) share its row when
  they fit, rather than starting another row below it.

## Type and emphasis

- Siblings in a row share size and weight; a text button in a row of small chips
  uses small text.
- Composer model and effort controls use the same rounded rectangular treatment
  as new-thread pickers. Fast uses a matching toggle with a visible pressed state.
- Header badges share the stock compact sizing while preserving semantic status
  colours and readable labels. Icon-only header controls expose tooltips on hover
  and keyboard focus, as well as accessible names; diff counts keep their signed
  numeric treatment.
- Agent names render through `AgentName`. Its `quiet` form keeps the
  surroundings' weight and ink for secondary text such as the process dock.
- An empty slot or state reads as empty: muted ink and the word, distinct from
  content.

## Focus

- Only controls show focus rings. Never make containers focusable to park focus.
  A row that must receive focus for a hand-off, as after loading earlier entries,
  gets `tabindex` for that moment and loses it on blur. Otherwise clicking its
  text would focus the row, and the next key press would ring it.
- A dialog may focus itself on opening. It is a container, so it never shows a
  ring.

## Tabs, dialogs and menus

- Panel tabs are text tabs; the selected tab is underlined in
  the accent colour. A lone tab still reads as a tab. Segments are for toggles
  inside content, such as Main and Side or the resume scope.
- In dialogs, labels use `Field`. Cancel is quiet and comes first, the submit is
  primary, destructive confirmation uses `danger`, and failures use `Alert`, not
  the warning `Notice`. Dialogs and menus share Nova corner radii, spacing and
  highlighted rows. Phone dialogs remain safe-area-aware bottom sheets.
- Render a server menu's structure, not a flat grid of identical rows. Presets
  list numbered slots with their key, show empty slots muted, put actions in a
  separate button row, and end each step with dialog buttons.
- Session metrics use compact, grouped label/value tables. Measurements align
  with tabular numerals; explanatory prose spans the section instead of leaving
  an empty label column. Preserve zero values, units, paths and server-provided
  details, with long content wrapping on phones.

## Content from the server

- Show what the reader needs, not wire text. Diff views drop the review's
  `update "a" -> "b"` operation line, because the view already says whether a
  file was created, deleted or moved. A tool-managed edit summary shows its
  counts as coloured diff counts rather than the receipt's diagnostic wording,
  and a wait's reason follows its targets as a separate clause.
- Placeholders and failure boxes appear only where their content can appear. A
  path attachment shows a preview box only when its name is an image; other paths
  keep the plain chip.

## Refresh and loading

- Keep shown content until its replacement arrives. A revision, refresh,
  reconnection or navigation never returns loaded content to a skeleton, an
  empty list or Ended.
- Navigation replaces only what belongs to the new selection; the roster and
  family navigation stay mounted.
- A read superseded by a newer publication is not an error. Discard its result
  and wait for the current reference; show only current failures.
- Loading and busy states take no in-flow row. As content arrives above or
  around the reader, keep their reading anchor and keyboard focus.

## Themes

- Every change must work in both palettes. Text, including syntax colours on
  tinted diff rows, keeps at least 4.5:1 contrast; `TestWebUIBrowserDiffs`
  enforces this for diffs.
- Before the theme script runs, the CSS gives a dark system its dark ground, so
  loading never flashes the light palette.

## Checking rendered results

Source review does not prove a layout. Build with `make webui`, then run the
browser tests in `apps/mekugi/internal/launch` with `MEKUGI_TEST_WEBUI_BROWSER=1`
and `MEKUGI_TEST_WEBUI_ARTIFACTS=<dir>` to collect screenshots. Check desktop
(1280px), tablet and phone (390px) widths in light and dark, with one item and
several, and with the longest realistic content (paths, lines, lists). For a
refresh, delay the replacement and check the old content stays. Measure geometry
with `getBoundingClientRect` in the page instead of judging spacing by eye. A
surface that no browser fixture reaches, such as the preset dialog, needs its own
rendered check before it is called done.
