# Terminal session

## Boundary

The attached terminal owns byte-stream decoding, pane geometry and focus,
renderer preferences, frozen source selection, clipboard requests and incremental
frame delivery for live sessions and offline replay. Feature painters supply
content, source annotations and hit targets; application integration routes
selected targets and intents to their owners.

[Session presentation](../sessionpresentation/SPEC.md) owns displayed identity and
Main admission; [session control](../sessioncontrol/SPEC.md) issues host operations.
Feature views own disclosure, navigation and animation.

## Input

The live decoder separates key sequences, SGR pointer reports, terminal color and
focus replies, and bracketed paste before pane routing. Paste bytes are opaque,
including shortcut bytes and incomplete control sequences; only the exact end
marker changes paste state. Dialogs and status views discard paste payloads while
the decoder still consumes their terminator. Terminal replies never reach the
composer or pane shortcuts. A lone Escape waits 40 milliseconds for continuation;
queued continuation bytes are drained before expiration is applied.

Replay has read-only input admission. Its decoder consumes fragmented SGR,
legacy mouse and navigation reports without treating their payload as playback
commands. Ctrl-C remains available after an incomplete report; a new Escape
starts independent disambiguation. [Playback](../../../../modules/sessionreplay/SPEC.md)
owns scheduling and semantic playback controls.

## Panes

Main starts at full width. Diff toggles the auxiliary column open and closed.
Pane preferences retain visibility and focus; transient selection and expansion remain local to the active frontend.
The bottom pane selectors are clickable and perform the same selection as
`Ctrl-B` followed by the pane digit. Only visible selector cells are active;
open dialogs and selection actions retain pointer precedence. The unread Diff
indicator is separated from its label and retains the selected tab highlight.

Geometry reserves the status row and a content-fitted roster before allocating
Main and Diff. Wide terminals show both columns; narrower terminals show the
focused content pane. A short terminal retains Main rows before roster or pane
frames. The roster uses four rows unfocused and up to 40% of the height when
focused. Its separator captures pointer gestures without storing a preferred
roster height. Column-divider dragging and prefix Left/Right resizing share the
retained split; saved Diff owns its file-navigator width.

## Selection and source copy

Dragging across text in the displayed transcript, the composer, a side-question dock, the
saved Diff, or a shared content dialog selects the visible text and offers
Reference (r/R), Copy (Ctrl+C, also c/C), and Clear (Escape) in the status bar or
dialog footer. Selection actions and the existing pane shortcut bar share bold key
labels and bullet separators. Only complete, visible action hints accept clicks.
Text selection uses an opaque, theme-aware background and preserves syntax
foreground colors.
Composer selection excludes its prompt and borders;
Diff selection covers only the source column, excluding the file navigator, gutters,
and line numbers, and keeps each row's `+`, `-`, or space marker. Dialog selections
copy visible text. Other Markdown selections use
[source-aware reconstruction](../../../../libraries/terminalui/SPEC.md), preserving
selected formatting and code whitespace while excluding decorative rows and gutters.
Unselected table cells remain empty, with a structural header and delimiter when
needed; a single selected cell without a selected separator copies just its content.
Forward and reverse drags use the same source ranges; wide graphemes
are never split. Pinned replies use the same annotations as transcript content.
A press in the Diff still reaches the pane, so clicks keep their meaning.

Reference from a dialog closes it and focuses the composer. Copy and Clear keep
the dialog open; `y` is also a dialog selection-copy shortcut.
Copy requests the terminal clipboard via OSC 52, and Clear leaves the draft intact.
Main, child transcripts, saved Diff, side-answer and dialog selections retain a frozen document,
including off-screen rows, until the selection is dismissed. Mouse-wheel scrolling
during a held drag extends the range through that document; dragging above or
below its scrollable viewport scrolls one row per motion event. Up/Down,
PgUp/PgDn and Home/End scroll an active scrollable selection without clearing it.
Scrolling after releasing the drag leaves the selected range unchanged. Newly
exposed rows retain source-aware Markdown copying and their original click targets;
saved Diff rows retain their gutters and change attribution. Static selections
retain their existing dismissal behavior. Resizing or resuming editing dismisses
the selection. Recognized Markdown file links in Main, child transcripts, and shared
dialogs open local source with the path, content, and read errors copyable, as
specified by [activity display](../../../../modules/activity/CONTRACT.md). HTTP(S) links still copy
their destinations in Main and child transcripts; clicking them inside a dialog still
does nothing. Missing local file destinations are not clickable.
Clipboard availability is controlled by the user's terminal.

The application binds Reference to the [conversation attachment pipeline](../../../../modules/conversation/SPEC.md#attachment-snapshots)
and labels selections using their original item or diff-row attribution. A frame
retains its source-copy annotations independently of selection highlighting.
Clipboard bytes and the painted-row cache advance only after a successful
terminal write. Failed writes leave pending clipboard delivery and the previous
frame available for retry. External-editor handoff invalidates the painted cache.

## Preferences

Successful resume restores the Main/right-column split, Diff visibility, keyboard
focus, interrupt-key lock and diff navigator width from
[session preferences](../../../../modules/sessionpreferences/SPEC.md). Geometry is clamped
by the current terminal layout. Roster height remains automatically fitted. Scroll
positions, filters, selections, drafts and transient live docks are not persisted.
The terminal adapter owns these fields; the preference module owns identity,
publication, debounce, flush and failure behavior.

## Acceptance

Consuming tests exercise pane switching and resizing, terminal replies and paste,
source-aware forward/reverse selection, frozen off-screen scrolling, original
click targets, Reference attachment delivery, clipboard requests, output-dialog
lifetime, interrupted replay reports and preference isolation. Real renderer
snapshots cover live and replay frames.
Failed-write checks cover clipboard and frame publication together. Live host
acceptance remains separate from offline renderer coverage.
