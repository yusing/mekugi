# Journal

## REQ-JOURNAL-001 — Per-thread milestone journals

Mekugi mode owns one durable milestone journal per stable thread. Passthrough is unchanged.
Items have router-assigned IDs (`j1`, `j2`, ...), nonblank UTF-8 text, an optional original
question, canonical author, router sequence creation/update values, `report_now`, `reported`,
and `flushed` state. A thread has at most 256 items and 256 threads are retained. Combined
question and text content is limited to 16 KiB per item. The per-response live progress budget
is also 16 KiB. Terminal flushes have a separate bound sized for all 256 items, including labels
and the author heading. Main completion flushes descendant journals in canonical agent-path order (stable thread ID
breaks ties), then its own journal. Each journal has its own terminal capacity. Capacity exhaustion rejects new journal state, not unrelated calls. When initialization hits
capacity, ordinary provider answers remain visible and journal finish returns an error.

An ordinary fork copies the source's latest journal at its first accepted normal Responses
request in the selected workspace, then evolves independently. It does not reconstruct the
historical fork point. Routing-session remaps and resumes keep the stable thread's journal.

Eligible non-strict ordinary function tools receive an optional `journal` array. Each mutation is
`add`, `edit`, or `delete`; additions and edits require nonblank text, edits and deletes require
an existing ID, and a malformed array rejects the host call before execution. The array is applied
in order atomically, then removed from executed arguments while the original call remains available
through replay. `report_now` emits a router-owned user-only **Journal update** labelled with
the item ID and author when known. Successful delivery marks that revision reported, not flushed.
It MUST remain eligible for a terminal **Journal flush**, whose heading identifies its known author
and whose entries identify their IDs. These labels distinguish journals from stock commentary
and reasoning summaries without rewriting stock output. Deletes are silent unless retracting
an already-reported ID.

An add/edit mutation may carry `answer: true`, with only the answer in `text`. The router
attaches the latest actual user message from that request's visible history, not instruction
or environment context. The agent does not repeat the message. Missing or oversized source
content rejects the answer mutation rather than inventing or silently truncating a question.
Omitting `answer` on edit preserves the attached question; false clears it; true attaches the
current source message. Delete, list, and finish reject a direct `answer` operand. The attached
question is retained by list, durable replay, restart, and forks, independently of later user
messages. Inference is request-local; concurrent threads and branches do not share its source.

Mekugi mode also exposes `functions.journal` with one operation: `list`, `add`, `edit`, or
`delete`, or `finish`. List is read-only and may address only a proven ancestor or descendant journal. Unknown
or conflicted ancestry fails closed. Mutations return router-assigned IDs. The dedicated tool includes `journal_ids` for any batched field mutations, independently of its main operation result. Journal calls are
router state operations and do not invoke an executor. The dedicated schema exposes the optional
batched `journal` field. Existing journal declarations anywhere in the tool catalog, including
nested additional-tool namespaces, reject built-in tool exposure.

Direct `functions.journal({"op":"finish","journal":[...]})` requests turn completion,
optionally applying the last atomic mutation array in the same call. Finish takes final mutations
only through `journal`; other operands must be unset or at their empty/default values. Agents call it alone after required tool
results arrive, rather than waiting or generating another final-answer turn. On a successful
response with successful journal results and no client-dispatched calls, it completes and
returns the terminal response without another provider request. Mixed client calls remain
host-dispatched and prevent completion; journal operation error results continue for correction.
Invalid or rejected mutations in a dedicated journal call return an `ok: false` tool result
for correction and prevent its primary operation. Batched fields on host-dispatched tools
still fail translation under the atomic field contract.
Failed, incomplete, or interrupted responses never complete or flush via finish.
Completion intent is response-local: replay, resume, and forks do not finish a new turn.
Finish is not exposed through runtime shell or Code Mode journal publication.

On a successful explicit main finish with no client-dispatched calls, the router emits a deterministic
descendants-first tree flush containing only unflushed revisions, including previously live-reported entries, skips it
when empty, then emits token metrics. Only successful terminal delivery marks a revision flushed;
edits clear both current-revision delivery flags. `list` exposes both flags. Finish ends the
turn without a follow-up provider request or a separately generated final answer. A child finishes without flushing and emits
`Journal saved: N pending, M already flushed` so collaboration result selection is nonempty.
Live updates remain immediate. Descendant revisions are read from durable journals at main completion,
including after router restart, and acknowledged only when main delivers them. Failed main delivery
leaves unacknowledged revisions pending. Only proven, unambiguous ancestry in the selected workspace
is included; ordinary forks do not inherit the source's child tree. Failed,
incomplete, and interrupted responses neither flush nor discard already-streamed provider output.
Router-owned messages use generated IDs and are removed from later provider input by exact ID.
Before adding journal results or notices to a streaming terminal with an absent or empty
output snapshot, the router MUST preserve completed streamed items in the projected snapshot.
Internal journal continuations MUST preserve client-dispatched calls and their paired
results in subsequent WebSocket history, under both native and CTP/2 protocols.

Mekugi mode forces `tools.update_plan.enabled=false` and removes `update_plan` declarations from
the request catalog, including nested additional-tool namespaces. Stock Planning/Tasks conflicts
are rewritten to journal guidance. Passthrough retains the stock tool and prompt.

### Runtime authoring

Journals record checkpoints and milestones, not plans or ongoing narration. Agent guidance
asks for a concise final report of findings, results, validation, or blockers, with superseded
entries reconciled. Agents mark answer items with `answer: true` and put only the answer in
`text`; the router supplies the original question. Live notices and terminal flushes label these as **Question** and **Answer**.
The router preserves authored Markdown rather than summarizing it. Each terminal item keeps its
ID separate from its body and indents all body lines under that item, including blank lines,
nested lists, paragraphs, and fenced code blocks.

Code Mode reserves `await journal({op, id?, text?, answer?, report_now?})`, also accepting
a mutation array. The parser preserves strings, comments, properties, and unrelated
identifiers, and leaves unparseable source unchanged for the executor to diagnose.
A single mutation returns its item ID; a mutation array returns the ordered item IDs.
Nested calls can use an added item's returned ID in a subsequent edit. Publication failures
throw rather than returning an execution envelope as a journal ID.

Bash and POSIX reserve `journal add TEXT`, `journal edit ID TEXT`, and
`journal delete ID`, optionally followed by `--report-now`. Shell commands record milestones;
use the dedicated tool, a structured tool mutation, or Code Mode for answer items.
Expanded operands remain individual argv values. A successful publication writes no script output. Invalid
mutations and unavailable publishers return errors rather than silently losing records.
There is no `commentary` alias. Other interpreters have no journal builtin.

Both forms reuse authenticated broker routes and private thread-bound discovery.
Shell routes use inherited `CODEX_THREAD_ID`; do not add publisher flags or inline
environment assignments to scripts. Concurrent workers share a thread route without
guessing original call attribution. Code Mode retains its per-call capability until
completion or expiry. Closing an already handed-off response does not cancel its publisher.

### Delivery failures

Required terminal journal messages must have retained replay provenance before an
explicit finish can complete successfully. If required retention fails,
the response fails instead of silently completing without the flush or child summary.
Unacknowledged revisions remain available for a later flush.

A provider final message is not a journal finish. The router neither suppresses that text nor
uses it to trigger a journal flush. Ordinary token-usage buffering remains bounded and releases
provider events unchanged on overflow.

### Acceptance

1. Mekugi catalogs omit Tasks/`update_plan` and project journals only onto eligible ordinary
   function schemas. Strict tools, provider-owned additional tools, collaboration, and
   user-messaging schemas stay exact; passthrough is unchanged.
2. Batched mutations apply atomically before execution, return assigned IDs, and disappear
   from host arguments. Replay restores the original call without executing it again.
3. CRUD, independent ordinary forks, resume, capacity rejection, and retained reporting
   revisions survive router restart in the selected workspace.
4. Cross-thread listing allows only proven ancestors and descendants, never siblings.
   It is read-only and does not copy entries or subscribe to their delivery.
5. Immediate notices are acknowledged after successful emission without consuming the terminal
   flush. A failed live or terminal delivery remains eligible for retry. Silent edits become
   flush-eligible again; deleting a previously shown ID with report_now emits a retraction.
6. Successful explicit main finish calls show only unflushed revisions, descendants first then main,
   including live updates, then eligible token metrics. Child finish calls save without flushing and
   retain a nonempty saved-summary. Finish makes no final-answer continuation request.
   Provider messages remain unfiltered and do not trigger a flush; failures and interruptions do not terminal-flush.
7. The native Codex spawn fixture proves that journal results survive client normalization
   and that the parent receives the child's synthetic summary after one child provider request
   containing finish and its last mutations, with no final-answer continuation.
8. Debug evidence separates applied mutations, runtime wiring, live rendering, and
   terminal flushing without recording journal bodies or private publication credentials.
9. Multiline Markdown stays within its terminal journal item. Answer items display the
   original question and a labelled answer in live and terminal delivery; question edits,
   clearing, list, replay, restart, and forks preserve the specified state semantics.
