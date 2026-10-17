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
The status panel consumes a detached session publication. It owns its selectable
rectangle, scroll position, metrics tab and retained-exchange selection, preserving
navigation across refreshes of the same panel and resetting it on replacement.
Close and refresh return versioned panel intents; painting and navigation never
read capture or issue account requests.
The terminal's stream cursors pace command, segment and public-reasoning output
separately from source-operation correlation and canonical completion.
Activity source feeds and terminal views are separately bound by the application;
terminal views consume detached publications and return output demands or intents.
They retain no source reducer, storage handle or application-session pointer.
Main-frame composition consumes detached session, draft and prompt values. Its
terminal owner allocates the transcript window, docks and composer, paints the
composer and captions, and returns geometry for pointer routing and delivery
receipts. It renders the attached Activity view and returns identity-addressed
output demands for application hydration before delivery. Painting has no backend
or storage capability. Live sessions and playback use the same frame. Shortcut
visibility and the keyboard-interruption lock stay with terminal state.
The outer compositor receives prepared pane rows and header/status facts. It owns
frame decoration, status-tab targets and final pane geometry. Prompt visibility
is calculated against that geometry and active overlays; only successful terminal
delivery makes a visible prompt ready for input.
Prompt layouts, preferred columns, masked secret editors and readiness belong to
the terminal. Layouts use stable call, question and approval identities rather
than model pointers. Delivery receipts identify the attachment and dock opening;
an old frame cannot ready a replacement, while ordinary publication updates keep
an already visible dock ready. Prompt keys consume detached values and return
targeted actions for application admission.
The [attachment owner](../sessionpresentation/SPEC.md#attachment-lifetime) retires
module state. Backward replay retains terminal focus and Diff visibility.
Descriptor-selected roster and auxiliary views define available placements.
Absent placements have no rows, selectors or pointer targets; restored focus on
an absent placement returns to Main. Main and child Conversation remain readable
without a roster contribution, and Main retains ordinary input ownership.
The [saved-Diff view](../../../../modules/changehistory/SPEC.md#saved-review-presentation)
supplies interaction and source-selection values; this attachment owns its terminal
emulation surface and final frame delivery.

Storage and observation work must not stall input, cancellation, rendering or
unrelated lifecycle delivery, including through shared queues or locks. Missing
metadata and exhausted observation capacity remain visible and bounded without
terminating unrelated work. Presentation introduces no execution path, capturer,
history store, accounting ledger or model-visible commentary.

[Saved-review loading](../sessionreview/SPEC.md) leaves ordinary input, previews and host lifecycle delivery
responsive. Restored input that depends on retained evidence waits for loading to
settle. Successful views replace the previous retained view together; missing
evidence or exhausted read capacity reports unavailability instead of empty
history. Attachment replacement rejects stale results without waiting for storage.
Attachments without a retained-review source still accept scope and lifecycle
updates. They expose no saved evidence; a referencing change cannot be loaded
without its source.

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
starts independent disambiguation. Queued continuation bytes remain part of their
report after a slow frame instead of becoming a standalone Escape.
[Playback](../../../../modules/sessionreplay/SPEC.md)
owns scheduling and semantic playback controls.

## Composer controls

The terminal adapts these controls to [conversation drafts](../../../../modules/conversation/SPEC.md)
and [session operations](../sessioncontrol/SPEC.md#interactive-operations):

| Input | Action |
| --- | --- |
| Enter / Tab | Conversation: submit or steer / queue for the next turn; both submit while idle. Shell Mode: both submit immediately, including while Main is busy |
| Alt-Up / Shift-Left | Restore locally waiting input ahead of the draft |
| Escape | Dismiss selection, picker or help, or resume transcript following first; otherwise interrupt, expediting uncommitted steers when eligible |
| Ctrl-C | Clear the draft, then interrupt, then quit when idle with no waiting input |
| Arrows | Move by grapheme/display row; at the first/last row, browse input history |
| Ctrl/Alt-Left/Right, Meta-b/f | Move to the previous word start or next word end |
| Ctrl-Up/Down | Move to logical line boundaries |
| Alt-Backspace/Delete, Ctrl-W | Delete the previous/next word; Ctrl-W deletes the previous word |
| Ctrl-K | Delete to logical line end, or its following newline |
| Ctrl-Z / Ctrl-Y | Undo / redo |
| Ctrl-V | Attach a PNG from the desktop clipboard |
| Ctrl-G | Edit the draft externally without submitting |
| Shift-Up/Down | Select adjacent model-advertised reasoning levels without wrapping |

The terminal maps edit keys and wrapped caret movement to source-coordinate
editor actions. Conversation retains the only ordinary editor and its undo/history
state. Secret input edits the terminal-local masked editor instead.

Word edits skip punctuation and whitespace without splitting attachment tokens.
Editing and the visible window follow the caret. Images, files, skills and selected
text have distinct token colors, retained by submitted host text elements; literal
lookalikes never gain attachment identity. Tokens stay on one row when they fit,
otherwise wrap at grapheme boundaries.

A paste containing exactly one absolute PNG/JPEG/GIF path, including shell quoting
or a local file URL, attaches that user-owned file and a trailing space. Other
pastes are text. External editing uses `$EDITOR`, then `$VISUAL`, then `vi`, with
arguments supported. Terminal/input ownership is released during the editor and
restored afterward. Failure preserves the original draft and names its recovery
file. Conversation owns binding reconciliation; adapter cleanup follows its
attachment-lifetime contract.

`?` in an empty focused composer opens shortcuts above the composer. `?` or Escape
closes them; another key dismisses them and retains its usual action. A mouse event
only dismisses help. Nonempty drafts and pasted question marks remain literal.
At a word boundary, `@` opens file completion and `$` opens skill completion.
Up/Down or Ctrl-P/N wrap selection; Tab/Enter inserts without submitting; Escape
closes without removing the token. Paths with spaces are quoted. Paste does not
open completion. Loading, empty and failed lookups remain visible.

Completion, skill management, settings, archive confirmation and response-copy
menus consume detached choices. The terminal owns selection, scrolling, hit
geometry and key interpretation, returning identified choice, search or dismissal
actions. Application integration retains acquisition and host effects. A slash
completion followed by submission settles its edit before deriving the submission
from the new publication.

An initial `/` opens the local command catalog. Typing filters; arrows select;
Tab completes only the command token, preserving arguments and attachments;
Enter executes; Escape closes unchanged. Arguments and paste close or bypass
completion. Local commands never become model prompts; unknown commands report
feedback. Shell Mode, selected by an initial `!`, has a red frame and Bash syntax
highlighting and disables attachment completion.

`/lock` and `/unlock` control the persisted interrupt-key lock. Locked input cannot
interrupt, discard waiting input or quit through Escape/Ctrl-C. Draft clearing,
undo, copying, contextual dismissal, explicit commands and steering still work.
The visible Locked indicator names `/unlock`.

## Questions and approvals

[Conversation](../../../../modules/conversation/SPEC.md#questions-and-approvals)
owns prompt lifecycle. Clicking a pending banner or Ctrl-B q opens approvals
before questions. The dock replaces live edits, occupies at most half of Main,
and accepts answers only after successful delivery of a frame that shows it.
Rendering an unseen dock beneath a content dialog or frozen selection does not
make it ready. Other pane focus retains Main's pending count. Hidden prompts leave
normal interrupt behavior intact.

Prompt docks yield to the composer when the terminal has no space for them.
Their requests remain pending until they can be shown and answered.

Questions show their position, full scrollable text, options/descriptions and
Other. Only visible options set column widths; narrow layouts omit descriptions
before answer text. Up/Down or Ctrl-P/N wrap; digits choose; other text edits Other.
Tab adds a synchronous option note. Enter records and advances, submitting at the
last question; Ctrl-] skips. Gaps require `Submit with N unanswered?` confirmation.
Left/Right navigate questions only outside text editing; ordinary and word-motion
keys otherwise move the caret. Escape closes a note before hiding the dock and
retaining answers. PgUp/PgDn reaches long question bodies and option lists.

Approval docks show the host scope, command/summary, non-root directory, reason
and choices. Digits/arrows select and Enter confirms; Escape only hides. Supported
command denials accept typed/pasted reasons; digits select choices while the reason
is empty. Other approval types ignore text/paste. Answer editors reuse literal
Unicode, paste, undo, attachments, references and external editing, but disable
completion and Shell Mode. Secret entry is masked and disallows external editing.
Ctrl-C and pane/queued-input shortcuts retain their ordinary behavior.

Pending banners preserve Main's editing; questions warn when a new-turn prompt
would dismiss older calls. Input labels distinguish answering, option notes,
waiting turns and parked drafts from approval waiting. Host delivery remains
separate from painted readiness and offline prompt tests.

## Panes

Main starts at full width. Diff toggles the auxiliary column open and closed.
The auxiliary view comes from composition. Without a contribution, its selector,
geometry and input targets are absent, and restored auxiliary focus returns to
Main. Live previews retain their own attachment theme and remain independent.
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

Ctrl-B then 1 selects Main, 2 toggles Diff, and 3 focuses Agents. Ctrl-B PgUp/PgDn
scrolls the displayed transcript or its active live source; the wheel targets the
pane under the pointer. Pane titles and status hints expose focus, scroll and
unread state. Main's title uses the sanitized host name, truncated to available
width without an unnamed placeholder. Child display hides and preserves Main's
draft, disabling typing, paste, submission, settings and keyboard interruption;
navigation and copying remain available. Reference returns to Main before insertion.
Roster selection leaves an open Diff intact. A blank row separates the composer
from content when height permits. Uncolored border labels use the text foreground.

The welcome is presentation only. Releases show `mekugi-v<version> • codex v<version>`;
the embedded release tag precedes module/revision metadata, source releases use
their module version, and development builds show `Mekugi <revision>` or `Mekugi dev`.
Unavailable backend versions are omitted. It remains while composing the first
prompt, including six-row Main frames, yielding to history, live edits, modals and
the full multiline composer.

### Live changes

`/live` toggles [provisional source views](../../../../modules/livepreview/SPEC.md);
`on` and `off` apply explicitly. Each launch or resume starts enabled; the choice
lasts only for that frontend. Hidden views reserve no space. Live source replaces
its owning thread's selected transcript above the composer, yielding to question,
approval, picker and help docks.

Each file gets at least 15 rows including its heading, plus one shared Live heading.
Short frames show a file-count summary. New files advance the visible window without
shrinking existing splits. Ctrl-B e cycles and pins the displayed thread's file
window against later arrivals. Wheel and Ctrl-B PgUp/PgDn scroll live source
independently of transcript and saved Diff. Reaching its tip resumes following;
Ctrl-B r resumes both source following and the newest window.

Each completed call remains for its own 1.5-second settling interval. Surviving
pinned files keep their window; expiry of the selected call follows the newest
remaining file. The transcript returns when no cards remain. Following tests keep
their own running state after the edit dock retires.

## Content dialogs

A dialog captures input above both panes without changing transcript follow. It
uses at most 90% of each dimension, or available width below 60 columns, over faint
uncolored panes. Its theme-aware fill and accent frame preserve syntax, diff and
selection colors. Close uses the top-right button, Esc/q or outside click.

Arrows/j/k, page keys, b/space, g/G and Home/End scroll; the wheel affects only the
dialog. Page membership, output versions and selected content arrive as detached
values from the application. The dialog requests content by its selection and page
identity; it never opens retained output or refreshes a source model while laying out.
Left/Right or clickable tabs choose pages. Tab paths retain nearby
directories and filenames. Each page keeps independent scroll, search and follow
state. Live pages follow until scrolled away; End or returning to bottom resumes.
`/` starts case-insensitive substring search, Enter finds and n/N advances,
revealing the matching wrapped row. `y` copies retained output, or source when
output is absent. [Selection controls](#selection-and-source-copy) govern frozen
content and Reference/Copy/Clear. Ctrl-B ! opens the newest displayed error;
arrows page its retained errors.

Recognized local paths are clickable without checking existence during painting.
Lexical detection can also link slash-shaped prose; opening validates the target.
Opening reads current regular-file UTF-8 contents, up to 8 MiB. Relative
paths require session workspace metadata; absolute paths and local file URLs are
accepted. Workspace paths display relatively, external paths absolutely. A
selectable path precedes syntax-colored content. Line/range suffixes remain in
titles, reveal the first line and highlight its number and left padding, not the
gutter. Markdown opens rendered content with a separate File tab; other files and
errors have one File view. Tabs keep independent navigation and copy original
bytes. Missing, binary, oversized or unreadable content shows copyable errors.
The dialog shows loading until the read settles; closing or replacing it retires
that result. Reopening rereads without monitoring.
HTTP(S) links copy destinations in transcripts and do nothing on dialog click.
The application resolves and reads files; rendering receives their content or error.

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
copy visible text without gutters, line numbers or frame padding. Other Markdown selections use
[source-aware reconstruction](../../../../libraries/terminalui/SPEC.md), preserving
selected formatting and code whitespace while excluding decorative rows and gutters.
Unselected table cells remain empty, with a structural header and delimiter when
needed; a single selected cell without a selected separator copies just its content.
Forward and reverse drags use the same source ranges; wide graphemes
are never split. Pinned replies use the same annotations as transcript content.
A press in the Diff still reaches the pane, so clicks keep their meaning.

Reference from a dialog closes it and focuses the composer. Copy and Clear keep
the dialog open; `y` is also a dialog selection-copy shortcut. Escape clears a
selection before closing the dialog. Selecting live dialog content pauses follow;
search and page changes dismiss that selection.
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
the selection. Local links in Main, child transcripts and shared dialogs follow
the [content-dialog contract](#content-dialogs).
Clipboard availability is controlled by the user's terminal.

The application binds Reference to the [conversation attachment pipeline](../../../../modules/conversation/SPEC.md#attachment-snapshots)
and labels selections using their original item or diff-row attribution. A frame
retains its source-copy annotations independently of selection highlighting.
Clipboard bytes and the painted-row cache advance only after a successful
terminal write. Failed writes leave pending clipboard delivery and the previous
frame available for retry. External-editor handoff invalidates the painted cache.

Reference labels identify selected message, composer text, side answer, status or
dialog content. Diff labels retain change IDs and selected gutter lines, preferring
new-file coordinates and using old-file lines for deletion-only selections. Composed
or multiple changes retain that attribution; unattributed rows say Selected diff.
Colliding labels across draft and waiting input gain counters. Mentions remain
atomic through undo, history, external editing and rejection; resume rebinds a
unique label to its submitted quote. Oversized/omitted quotes report notices.

`/copy` selects the active thread's latest completed response, including history,
as whole Markdown, fenced blocks or blockquotes. Bounded previews never truncate
clipboard content. Streaming and other threads cannot replace that response.
Enter copies and Escape cancels; absent responses show an explicit empty state.
Copying during work neither interrupts nor submits input.

The `/btw` side-answer dock leaves ordinary input targeting Main. It shows elapsed
Answering progress until settlement and preserves truncation/omission notices.
PgUp/PgDn scrolls; frozen selection pins the question. Reference, Copy and Clear
exclude decoration and preserve Markdown source. Escape clears selection, dismisses
prompts/menus, then closes the side answer under Conversation's cleanup contract.

## Preferences

Successful resume restores the Main/right-column split, Diff visibility, keyboard
focus, interrupt-key lock and diff navigator width from
[session preferences](../../../../modules/sessionpreferences/SPEC.md). Geometry is clamped
by the current terminal layout. Roster height remains automatically fitted. Scroll
positions, filters, selections, drafts and transient live docks are not persisted.
The terminal adapter owns these fields; the preference module owns identity,
publication, debounce, flush and failure behavior.

`--ansi-faint=auto|on|off` selects terminal-local dimming. Auto disables ANSI faint
when `mosh-server` is in process ancestry; detached multiplexers may require an
explicit override. Detection scans no unrelated sessions. Unsupported faint uses
muted text and agent-color variants without changing semantic or syntax colors.

## Notifications and lifecycle titles

Codex-compatible titles mark Main's running work; pending questions, approvals and
unsupported requests take precedence with `Action Required`, even when hidden.
Settled Main turns remove the spinner; child completion does not settle Main.
Herdr owns seen/unseen completion indicators. Exit and external editing clear titles.

The application reads effective workspace `tui.notifications`,
`tui.notification_method` and `tui.notification_condition` without writing config.
Only successful live Main completion and eligible approval, synchronous-input or
async-question events notify; pending questions take priority and history/duplicates
do not notify. [Notifications](../../../../modules/notifications/SPEC.md#desktop-eligibility)
owns eligibility. Focus reports never become composer input. `osc9` emits OSC 9,
`bel` emits BEL, and `auto` uses OSC 9 in supported terminals/Herdr, otherwise BEL;
tmux outside Herdr receives passthrough-wrapped OSC 9. Text is generic. Failures
report auxiliary notices; disabling desktop signals leaves lifecycle titles active.

## Offline playback

Interactive replay requires terminal stdin/stdout, restores terminal modes on exit,
and always labels streaming as simulated. Space or `p` pauses, `+`/`-` changes speed,
`[`/`]` seeks, and `r` restarts. Pane controls and focused Ctrl-E disclosure remain
available. Dialogs retain navigation and close controls; outside them Escape or
`q` quits, and Ctrl-C always quits. Wheel reports scroll Main. No control submits
host intent or opens an execution connection.

`--headless` paints to a discard sink at `--width`/`--height`, default 160 by 48,
bounded to 20–1000 columns and 8–500 rows. It emits JSON timings on stdout, progress
and coverage on stderr, and no terminal bytes. Frame timing includes pacing,
layout and writes, with writes measured separately; terminal backpressure is
absent. Compare identical inputs, seed, speed and size, using 1.0x for representative
latency. Acceleration changes batching, not recorded times, and cannot establish
original wall-time latency. Profiling is [opt-in](../../../../docs/contributing/development.md#profile-live-sessions-and-replay)
and creates no automatic profile files.

## Acceptance

Consuming tests exercise pane switching and resizing, terminal replies and paste,
source-aware forward/reverse selection, frozen off-screen scrolling, original
click targets, Reference attachment delivery, clipboard requests, output-dialog
lifetime, interrupted replay reports and preference isolation. Real renderer
snapshots cover live and replay frames.
Failed-write checks cover clipboard and frame publication together. Live host
acceptance remains separate from offline renderer coverage.
