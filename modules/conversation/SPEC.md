# Conversation

Conversation owns editable drafts, attachment identity, message and public-reasoning
interpretation, question and approval records, queued payload ordering and bounded side answers. Main, question answers,
approval reasons, queued input and side questions share the composer model.

[Terminal session](../../apps/mekugi/internal/terminalsession/SPEC.md) owns geometry,
focus, keys, clipboard acquisition and external-editor handoff; feature views paint
Conversation values. Detached prompt views contain no draft text, secrets or live
handles. The application admits
input, retains live requests and performs acquisition; [session control](../../apps/mekugi/internal/sessioncontrol/SPEC.md)
issues and settles host operations. Codex owns execution, acknowledgements and
durable input history; the backend owns response encoding. Displaying a child
transcript does not transfer Main's input or prompt ownership.

## Transcript observations

Main and child messages and public reasoning share one interpretation across live
observations, restored history and simulated replay. Conversation correlates admitted
facts in application-owned attachment, thread and turn scopes. Its composed Codex
binding supplies the same transcript owner to native startup and offline replay;
Activity consumes the resulting publications without owning conversation state.

Completion releases turn correlation; completed public-summary bodies follow feed
retention. Provider thinking may precede a known turn, and observations may arrive
without a start event. Closed-turn starts and deltas cannot revive correlation.
Authoritative late completions update their original identities without consuming a
newer turn's thinking or changing its answer selection.

Historical projection is stateless and preserves item identity and order without
replacing live state or promoting older-page commentary. The Activity
[presentation contract](../activity/CONTRACT.md#authored-content) owns child-answer
selection, public-summary disclosure and terminal pacing;
[replay](../sessionreplay/SPEC.md#recorded-clock-and-synthetic-streaming) owns seek
and reset behavior. Omitting Activity's retention does not remove the transcript.

## Drafts and history

After an eligible completed reply, an empty ordinary composer can show an optional
next-message prediction. It remains separate from the draft, attachments, history
and undo state until accepted. Tab accepts it as editable text without submitting.
Typing or replacing the draft dismisses it; clearing that edit does not resurrect
the old prediction. Prompts, completion menus and queued input retain precedence.
Predictions are transient and belong to the originating thread and completed turn.

The composer retains text, a byte-based caret, attachment bindings, undo/redo and
local input history. File references, skill references, selected-text mentions and
image placeholders are atomic during navigation and deletion. Explicit bindings,
never matching ordinary text, supply their identity. Character and word operations
respect Unicode graphemes, including combining marks beside attachments. Images
are numbered in draft order after insertion, deletion, joining or external editing.

The current ordinary draft publishes a detached snapshot and monotonic content
revision. Edits, caret changes, replacement, undo, restore and taking the draft advance that
revision; restoring identical text does not revive an earlier submission target.
Question and approval editors are excluded from ordinary-draft publication.
Their active non-secret draft has a separate detached publication naming its call,
question or approval and content revision. Parked history and secret buffers remain
private to their owners. Prompt key interpretation returns targeted actions;
Conversation applies admitted choice, note and editing changes. Source-range
deletions, insertion, caret movement, undo/redo and history recall share one editor
action contract. Actions name the ordinary draft or exact active prompt and its
revision; stale actions leave both content and editing history unchanged.

Undo groups a typed word, a repeated deletion run, a paste, an attachment, a clear
or an editor save. It retains at most 100 edits and resets on submission. Local
input history retains at most 100 entries. Browsing restores the saved draft and
caret after the newest entry. Live history keeps attachment bindings; resume
hydrates from host thread history. The interactive adapter also recalls ordinary
text from Codex’s shared composer history across fresh launches. Acknowledged
ordinary input and the ordinary exit draft join that history when Codex history
persistence is enabled. Persisted recall is text-only, not automatic draft
restoration; attachment bindings and answer drafts remain process-local.

Joining drafts preserves send order, separates nonempty entries with newlines,
retains immutable attachment snapshots and keeps the last entry's caret. Rejected
or withdrawn input can return before newly typed text without losing attachment
identity. Application-only payload metadata stays outside draft state.

External editing preserves exact image and selection placeholders when moved or
removed. Invalid UTF-8 or ambiguous image/selection placeholders, including
duplicates, leave the draft unchanged. File bindings survive unrelated edits, and
repeated labels retain their distinct paths in order. Extending a filename does not
bind its old prefix. Skills bind only complete references.

## References and attachments

### File and skill acquisition

Completion replaces the whole token around the caret, retaining its byte span even
when the caret is inside the token. Bound attachments remain opaque. Dismissing a
target keeps it closed until the target changes; changed queries retain only
matching results. Choosing a file or skill preserves its exact identity through
ordinary undo and attachment handling. Terminal completion and management controls
follow [composer controls](../../apps/mekugi/internal/terminalsession/SPEC.md#composer-controls).

The attached Conversation owner retains completion targets, admitted choices and
full response-copy values. Application integration retains searches and pending
skill writes. Completion publications retain query targets and choice identities.
Highlight and viewport navigation belong to the renderer, including preserving a selected skill
or command as results change. Accepting a choice names its identity and originating
session/menu publication before applying the token edit or skill write. A delayed
choice cannot target a replacement menu, workspace or draft.

The application roots acquisition in the active thread's absolute workspace,
never the router directory. Ordinary file lookup uses Codex's ignore-aware search.
`@!` includes ignored files, excluding VCS metadata and directory-symlink traversal.
Cancelled or stale results cannot replace another workspace's current selection.

Enabled, exact, unambiguous `$name` references bind at a word boundary or submission,
including pasted input. Unknown, disabled or ambiguous names and shell variables
remain literal. A pending catalog lookup delays submission; failure reports
unavailable references without suppressing available skills from another source.

When available, `skills-mgr list --codex` runs in that workspace with the wrapped
environment. Managed names take precedence over same-named Codex entries.
Submission or queueing resolves them with `skills-mgr get --codex NAME`, including
explicitly invoked skills omitted from the managed model catalog. Native entries
retain their exact metadata source path through editing and history. Managed
placeholders retain their metadata identity while acquisition reads the manager's
instructions rather than placeholder text. Repeated names attach once.

`/skills` offers Codex's List and Enable/Disable actions. Listing inserts `$`;
management filters enabled/disabled entries by display or canonical name. Selecting
a toggle saves its native path. Enabled state changes only after success, failures
stay visible, and closing reloads the catalog. Managed configuration belongs to the
manager; it never uses invented native paths. Closing leaves an already-sent save
pending. Its result updates only the matching workspace catalog; a late failure
remains a notice without replacing a new query's or reopened manager's error.
Plugin browsing is outside this picker.

### Attachment snapshots

Submission and queueing snapshot selected text files and directories from the
active thread's absolute workspace. Repeated paths attach once per draft. Frames
carry source paths and byte ranges, prefer line boundaries and split long lines
only at UTF-8 boundaries. Contents remain user input, never developer or system
instructions. Queued payloads and host history retain the submitted bytes across
fork, resume and provider changes without reopening their source. Recalled editable
bindings take a fresh snapshot when submitted again. Final submission gives each
envelope a durable identity independent of its contents. Identical submissions
remain distinct; request refresh, forks and resume preserve the submitted identity.
Version 1 envelopes remain readable without inventing a submission identity.

Native and managed skill instructions use the same framing, budgets and omission
receipts as files, never a synthetic native path or name-only replacement. Replay
recovers immutable instructions and identity without rerunning the manager.
Host text elements retain file/skill byte ranges relative to each text part;
native skill metadata can restore unambiguous bindings when elements are absent.
Explicit elements take precedence for repeated labels; absent metadata leaves
lookalikes literal. The backend binding decodes these elements and question-reply
envelopes into neutral composer spans and question identities; display adapters
map those values without decoding the host protocol again. Oversized or unreadable instructions produce omissions rather
than truncated instructions.

Each draft has a 192 KiB encoded attachment budget, half reserved for omission
notices. Submission identity must fit this envelope budget; an oversized supplied
envelope remains unsent with a notice rather than becoming ordinary prompt text.
File reads are bounded to 96 KiB and content frames to 24 KiB. Unreadable,
non-regular/non-directory, non-UTF-8, NUL-containing, oversized and over-budget
sources produce explicit omission frames; the composer reports the first omission.

Directories attach deterministic name-only trees, never recursive file contents.
Root children are depth 1 and their children depth 2. Deeper descendants and child
symlink targets are not traversed. Ancestor and nested `.gitignore` rules apply,
with ancestor lookup stopping at the nearest Git root. VCS metadata remains
excluded even for an ignored-picker reference. Git global excludes and repository
info/exclude do not apply.

Trees list at most 256 entries and 16 KiB while scanning at most 4,096 entries,
including ignored names. A listing exceeding the remaining scan allowance is
omitted whole rather than selecting a filesystem-order subset. Incomplete trees
carry an explicit truncation marker; a fully filtered tree is identified as empty.
Ignore input is bounded to 64 KiB across at most 64 ancestor levels and visited
children. Unavailable ignore rules explicitly omit the affected listing. Directory
snapshots share file envelopes, receipts and content dialogs.

Selected-text mentions follow file frames in the same envelope, preserving their
label and optional source path. Selections over 96 KiB are refused with a notice.
A quote exceeding the remaining envelope budget becomes an explicit omission
frame, reported by the composer. Question answers use the same bound tokens and
quoted attachments as Main.

Submissions carrying attachments or skills, including stacked input and encoded
snapshots, must fit a conservative 1 MiB UTF-8 text budget. Larger input returns
unsent with an actionable notice. Ordinary text limits remain Codex-owned. This
bounds attachment growth, not the context size of an existing conversation.

## Questions and approvals

Main-thread questions and approvals belong to one attached session, with stable
host identities and detached views. Restored questions retain their identities and
resolution rules without opening an editor or issuing a request. Views and replay
facts cannot revive live requests.

The composed Conversation service owns question state in the attachment scope.
Live messages, restored history and offline playback share question admission,
reply matching, supersession and transcript suppression. Before a matching input
changes an active call, the renderer parks its editor; the model then returns
discarded ordinary drafts for local history. Secret editors stay in the renderer.
Admitting the first host identity preserves that state. Successful Main replacement
retires it; unresolved local authentication requests are explicitly reattached with
their original identities, while host questions from the retired thread stay retired.

Questions prefer waiting synchronous requests before asynchronous calls. Approvals
are ordered oldest first and take precedence when explicitly opened. Opening either
prompt parks Main's text, caret, undo/redo and history navigation. Hiding, answering
or switching prompts restores Main without merging answer text. Every non-secret
answer and denial reason retains its own editor across navigation and hide/reopen.
Only an empty, idle Main composer can yield automatically. Explicit hiding
suppresses automatic reopening until a new request arrives. Visible readiness and
interaction follow [questions and approvals](../../apps/mekugi/internal/terminalsession/SPEC.md#questions-and-approvals).

### Questions

Ordinary answer intents name a pending call and question, with an explicit choice,
editable draft or skip. Retired identities, unavailable choices and secret questions
reject ordinary answer input. Submission names the call and explicitly confirms
incomplete answers; it does not depend on which prompt the terminal has open.

Each question offers its named options and free text. Synchronous options can carry
a note. Answering advances to the next unanswered question; submitting with skipped
or incomplete questions requires confirmation. Sending removes a call from
actionable counts without claiming delivery. Async calls remain open across turn
end. An ordinary prompt committed in a new turn supersedes older open calls;
steering does not. A matching committed reply resolves only its question. Rejection
restores answerability and the parked drafts.

External resolution, interruption or supersession saves discarded non-secret
drafts to local input history; secrets are discarded. Secret answers and question
reply envelopes never enter ordinary history. Reply envelopes do not create
duplicate message bands. Questions never count as final answers or complete turns.

Synchronous calls receive one response with the original host identity. Options,
notes, free text and skipped questions retain Codex's answer semantics; an
all-skipped call still sends an empty response. Hidden synchronous requests keep
ordinary input queued instead of steering. Async answers stay batched by call and
are committed by thread, item and question index.

Images and selected-text snapshots accompany answers. Images are numbered across
the batch, survive rejection and remain host-owned after submission. Sync
attachments follow the completed response as a companion input. Queued replies
can be reconciled before dispatch; transmitted input stays host-owned. Pending
progress distinguishes queued or sending input from committed delivery.

### Approvals

Approvals from Main and child threads retain host order and scopes. Commands offer
the host's supplied decisions, or accept, a proposed command-prefix amendment and
cancel when no list is supplied. Network wording follows the requested host scope.
Edits offer once/session approval or cancel and name the matching live item paths.
Permissions offer turn, strict-review turn, session or an empty grant. Terminal
input requests name the running terminal and quote the proposed input.

Approval decisions name a pending request and one of its published choice identities.
A retired request or unavailable choice leaves current approvals unchanged. Denial
reasons are explicit ordinary input; choosing a request never reads another prompt’s
editor. Local guard requests receive attachment-local monotonic identities.

Unknown choices leave an unanswerable request visibly blocked. Supported command
denial can carry a reason under the [backend execution contract](../../backends/codex/SPEC.md#execution-authority).
External resolution and turn end clear pending approvals without claiming approval
or denial. Approval facts decorate the associated host item rather than create
another transcript event. Auto-review facts use their exact target item.

[Remote-write](../remotewriteapproval/SPEC.md) and [sudo](../sudoapproval/SPEC.md)
approval owners define their policy, grants, expiry and authentication behavior.
Local authentication uses a separate secret editor; answers never enter neutral
draft state, views, answer records or durable history. Live handles, sockets,
response payloads and secrets stay in adapters.

## Queued and submitted input

Unsent steers precede next-turn input. Ordinary entries join in send order; a
question reply or compaction command remains its own submission. Conversation
retains queued compaction intent, not its execution lifecycle.

Answers steer during an active turn, start a turn when idle or queue until input
admission permits delivery. Deferred answers wait for a tool's generated input to
finish, not for execution to finish. Ordinary prompts retain instant interruption
and can pass deferred answers. Payload snapshots retain attachment bytes and
neutral question identity until [session control](../../apps/mekugi/internal/sessioncontrol/SPEC.md#submission-and-interruption)
settles submission. That owner also defines interruption, rejection, native-shell
recovery and session replacement. A rejected or withdrawn payload remains editable
and returns before newer drafts; committed input is never restored or resent.

Queue batches, submitted-payload matching, acknowledgement history and restoration
belong to this session owner. Restoring ordinary input while a prompt is open
updates the parked Main draft, preserving the active answer or denial reason.
Submitted payload identities are independent of renderer sequence numbers.

## Side questions

`/side` opens a fresh ephemeral snapshot of Main in the shared tab bar. Only one
side conversation is open at a time. `/oneoff TASK` uses the same slot for a
work-capable ephemeral fork, selects it on submission, and labels it One-off.
Creating either kind when the slot is occupied offers Replace or Cancel;
Replace closes the previous side before creating a distinct host thread, while
Cancel leaves it unchanged.
Selecting Side displays its conversation through the shared Main-pane transcript
and routes ordinary composer input to Side. Main and Side retain separate drafts;
selecting a subagent remains read-only. Side settings are the effective model,
reasoning effort and service tier captured from Main when its snapshot is created.
Settings and Main-only commands require returning to Main.

Pending Main input settles before the snapshot; already-running Main work continues.
Later Main activity does not enter follow-ups. Codex owns ephemeral history and
context. Side answers request no tools, disable environment access and use read-only
permissions without approvals; Codex does not guarantee that non-environment tools
are unavailable. Answer-only Side events never enter Main's questions, notifications
or transcript.
One-off inherits Main's tools, environments and permissions and uses the ordinary
Activity, change-evidence, questions and approval workflows with its own host
identities. Its instructions limit work to requests in the fork. Main remains
independently usable, and no one-off result is submitted back to Main. Tool edits
and other external effects remain real and may affect their shared workspace.

Completed Side and One-off results remain selectable while idle. One-off supports
host compaction independently of Main. A running answer refuses another submission
without consuming its draft. Failures remain visible and rejected input retains
attachments for retry in the same conversation. Rejected attachment companions
for a One-off descendant retain their original recipient in the waiting queue.
Enter in an empty One-off composer retries one companion; closing the slot cancels
queued retries.
If an answer-only Side requires compaction, Main stays
untouched: compact Main and create a fresh side snapshot.
The selected side composer shows loading, answering and failure state. Escape closes
Side after dismissing selection or menus, interrupting only its running turn and
unloading its host thread, without archiving or deleting it.

[Session control](../../apps/mekugi/internal/sessioncontrol/SPEC.md#session-replacement-and-exit)
retains delayed fork/start correlation and cleanup after close or Main replacement.
The transient conversation retains at most 256 KiB and 256 items across questions
and answers, dropping oldest exchanges with a truncation notice. Side membership,
answers and drafts are invocation-local and are not restored on resume.

`/btw QUESTION` owns a separate answer-only thread and temporary answer dock.
It snapshots Main independently and never reuses, replaces, blocks or closes the
Side/One-off slot. Repeated `/btw` requests follow up its own conversation, and
Escape closes only its dock before affecting the displayed conversation. Main,
Side and One-off input remain usable while the answer runs. Its bounded answer
history, pending request, failure recovery and delayed cleanup have independent
lifetimes. Rejected input returns to its originating composer, or Main if that
side conversation has closed. Main replacement closes both transient conversations.

## Lifetime

Conversation writes no durable records. Submitted attachment envelopes remain
with host user input; local edit history remains attachment-lifetime state. One
attached-session editor owns the active draft and local history; parked editors
and submitted payloads are detached values.

Conversation's scoped owner holds ordinary editors and pending approval records.
Successful Main replacement carries editable input, its local history and still-live
approvals forward without carrying submitted operations or host questions. The
adapter preserves original approval handles and scopes. Secret editing uses a
separate adapter-local editor, never the ordinary editor or its history.

Clipboard image and managed-skill acquisition, plus image cleanup, remain with the
session adapter. Cleanup accounts for active, parked, queued, undoable and
side-question drafts before reclaiming unsubmitted files. User-owned files are
never reclaimed; submitted image files remain available for host resume.

## Acceptance

Neutral checks cover mixed-token and grapheme edits, undo/history isolation,
external-edit failures, joining, question commitment/rejection, approval choices,
detached views, secret exclusion, completion admission and skill filtering.
Application acceptance covers queue restoration, prompt editors, side questions,
attachment acquisition/submission and replay. Terminal snapshots remain
renderer-owned; offline prompt checks do not establish host delivery.
