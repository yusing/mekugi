# Filesystem and output boundary

## CTR-BOUNDARY-001 — Filesystem and output boundary

The root library boundary owns workspace authorization, evaluation diagnostics, completed
results, staged commit and rollback coordination, and translation for
`REQ-GUIDE-001` and `REQ-OUTPUT-001`. Persistent Codex edit, shell, read, search, and inspection
guidance and CTP/2 representation guidance use `contrib/codex/file-editing-instructions.md` as their
shared reference template, including commentary routing. The adjacent Astra and default
editing-workflow files own model-specific editing, shell-submission, planning, target-reuse,
and target-acquisition guidance. General task autonomy, prose style, and validation policy remain host- and task-owned.
`contrib/codex/instructions.go` renders
the selected workflow; the router supplies each request's model and configured transport.
Tool descriptions retain only call-local contracts and request-specific schemas. The router
renders dynamic rejected-script references and the recovery instruction from the adjacent
recovery template only for actionable evaluator rejection diagnostics.

The root-scoped workspace boundary used by authorized library callers owns a pinned `*os.Root`,
a root-relative cwd, root-scoped reads, staging, commit, and rollback. Relative script paths
resolve from cwd; absolute paths become root-relative identities only when within root. Lexical
and symlink escapes fail. Initial inputs cross into that boundary only after a regular-file check
and strict UTF-8 decoding.

Normal router translation is outside that confinement boundary. It supplies an optional canonical
metadata directory to `TranslateForHostAt`, performs ordinary host path resolution without a
router-owned filesystem capability, and never falls back to router cwd. Without a selected
directory, only absolute operands are valid. Retained private `@shell` application is the confined
router exception and uses `ApplyForHostRoot`.

The router chooses transport session identity from an explicit `session-id`, then a stable
`prompt_cache_key`, and only then a request-scoped client request ID. Neither that identity nor
a shared thread owns historical translation. `internal/router/mekugi_store.go` owns versioned,
durable replay records scoped to the selected canonical metadata directory, or the explicit
no-directory state. Call IDs select records; replay validates the exact carrier kind, name, and
payload before restoring the model-visible item. A conflicting mapping or corrupt record fails
routing rather than guessing. An absent legacy record leaves an ordinary unknown host call intact.

`internal/router/mekugi_history.go` builds one ordered visible-history view per accepted request.
Recovery, target aliases, and executor confirmation use only that view and calls evaluated in the
same response. Resume and forks resolve their inherited carriers from the same workspace store;
they do not clone session maps or import hidden parent calls. Input truncation or compaction
removes unavailable ancestry from the next view, never from another request or the durable store.
Concurrent requests cannot change each other's view. Rejected reconciliation publishes no partial
confirmation or ancestry changes. Output-only history requires an unambiguous retained call record.

Completed call records are durably written before their carriers are exposed, including per-call
SSE completion before a terminal response. Partial inputs are never evaluated or retained.
Consistent later completion metadata may finalize a record without changing its translated mapping.
The store uses private filesystem permissions, cross-process locking, atomic replacement, and
synced writes. Durable capacity rejects new records rather than evicting resumable history;
memory-cache eviction does not delete durable records. Shutdown leaves replay records intact but
still releases process-owned runtime resources. Replay performs no translation or execution and
does not revive shell processes, continuation handles, or expired private script capabilities.
Background Responses requests reject before upstream forwarding because
the router has no retrieval boundary for their eventual result. Malformed SSE state is
sticky and cannot be overwritten by a later terminal event. `internal/responses` owns the
shared event-kind and terminal-evidence classification. Transports retain their distinct
exchange, steering, and delivery lifetimes: an error can end a WebSocket exchange without
being a successful response, and a steering acknowledgement is not a response terminal.
JSON body status and SSE event type retain their existing, different authority.

`review.go` renders per-file review diffs from completed engine changes, separately from
the bounded final-state projector and executor patch. Host finalization withholds those
diffs on failure. The router carries that projection in immutable replay records.
`internal/router/mekugi_changes.go` owns the workspace change index, thread-local ID
streams, correlation-linked attempt membership, and persisted execution receipts.
It shares the replay store's lock and durable-write discipline. Reservation precedes
evaluation; attempt membership is published only after its replay record is durable.
Confirmation can repair interrupted membership from matching durable replay facts in
recovery-attempt order. Idempotent index retries still synchronize the directory.
Receipts are written only after full request reconciliation and never influence recovery
or target aliases. `internal/router/shell_changes.go` owns the read-only `hchanges read`
interface, range expansion, views, and snapshot-bound pagination. It takes a shared
read-only lock only to snapshot the index, then reads immutable replay facts and renders
outside the lock. Its Go shell dispatch
reuses the bundled shell tokenizer; the authenticated manifest pins the store location.
No model-visible schema, public plugin implementation, or executable frontend is added.

`internal/router/session_inspect.go` owns offline logical-session projection for
`REQ-SESSION-001`; `cmd/mekugi` dispatches its read-only command before router startup.
It reads rollout call identities and validates records through the replay store's reader,
without opening its writable lifecycle or constructing a recovery view. It reuses stored
translation facts and the request-replay exact-report confirmation helper, including
completed host envelopes, never derives execution from carrier code.
Bounded text projection remains separate from sanitized transport metrics.

Transport capture is auxiliary: tokenization or durable-write failures cannot replace a successful
tool result, rejection diagnostic, read or search result, or response, while request cancellation
still propagates. An explicitly requested capture file that cannot be opened fails startup.

The router exposes only hpatch, hpatch_recover, and shell beside the displaced Code Mode `exec` carrier.
`hpatch` remains the native engine contribution. `hpatch_recover` is a router-owned recovery contribution. Shell, hcat, hgrep, hsymbol, and inspect_file are
JavaScript- and TypeScript-authored built-in plugin contributions compiled by Bun into one
embedded JavaScript module with the reserved `builtin.shell` identity. Shell is model-visible;
hcat, hgrep, hsymbol, and inspect_file retain snapshot-backed implementations but their
specifications are private and they have no executable frontends. Configured user tools remain
model-visible. Portable verified-row, compact-syntax, source-classification, Go-lexical, and
shell-header mechanisms come from the same authenticated Go-built shared core available to
configured plugins under `REQ-PLUGIN-001`; filesystem, process, parser-coordinate, and carrier
policy remain with the owners described here.

The plugin runtime snapshots and validates the immutable built-in module before user
declarations, then applies one normalized registry path to all executable
contributions. The registry projects only model-visible tool definitions. Configured
executor-backed plugins retain wrapper and frontend dispatch. Built-in shell uses the fixed
executor-side locator and a direct per-thread runtime path; its private commands execute from
that worker.
Request rewriting installs the projected hpatch, hpatch_recover, and shell definitions, removes the native
exec-command contract, and rewrites received Responses instructions from the central guidance
source. Native model protocol omits its leading CTP/2 section and stops after that ordinary rewrite;
CTP/2 injects the complete source and transforms eligible model-visible strings under
`REQ-CTP-001`.
Private contribution descriptions are execution contracts, not a prompt source. Passthrough
mode loads no registry.

The shell carrier preserves one physical line containing one static external implicit-default-Bash
command, with no shebang or directive, as the direct Codex exec command. Every other program emits
`shell <interpreter> <program>` in Codex's exec context. The fixed `cmd/shell` locator reads the path
`$MEKUGI_RUNTIME_DIR/mekugi-runtime-$CODEX_THREAD_ID` and replaces itself with the authenticated
snapshot worker stored there by the router. For Bash and sh selectors, a router-owned `mvdan/sh`
runner
parses `LangBash` or `LangPOSIX`, preserves shell-owned expansion and composition, and intercepts
private command argv without launching another router worker. Other interpreters retain the
plugin executor path. The worker derives relative-path resolution from its actual current
directory and leaves sandbox and permission enforcement to Codex. Absent request-specific exec
parameters, the shell carrier sets neither an environment override nor a working directory. A
direct `apply_patch` owner, missing Node.js runtime, invalid embedded declaration, or configured
wrapper failure rejects startup or rewriting before forwarding.

The hcat built-in accepts one path argv and an optional separate inclusive range argv.
Shell quoting owns whitespace and metacharacters in paths. Hcat has no multi-file input or
batch result format; the model batches reads as separate commands in one shell script.
Rendering streams fixed-size chunks, validates UTF-8 across the complete regular file,
and buffers only selected lines. The shared verified-row accumulator counts exact formatted
current output with the pinned GPT-5 tokenizer. Its default admits through the 15,000-token
soft limit with one complete-row overshoot through 15,500; an explicit token budget is strict.
Reader preview mode bounds displayed UTF-8 prefixes without changing full-source row identities.
The accumulator retains current and stock rows as one pair.
An omitted row seals output growth while hcat continues the existing stream for file and range
validation.

The hgrep built-in receives ordinary shell-produced argv. Its TypeScript implementation
rejects incompatible source and output modes and invokes installed ripgrep through
`--json --no-config`. Ripgrep alone owns search selection. The implementation consumes match
and context events, renders complete verified rows, deduplicates only identical path-and-line
results, and provides no fallback search implementation. Shell, rather than hgrep, owns
pipelines, redirection, and command composition. Hgrep uses the same verified-row accumulator
after result deduplication and terminates ripgrep when the accumulator rejects a row.

The hsymbol built-in owns verified language-token selection and verified-row rendering around one
installed semantic query. It canonicalizes the explicit workspace, or executor cwd when omitted,
confines input and returned files to that workspace, and accepts current line numbers or verified
rows. It uses the same pinned parsers as inspect_file to select an
exact token before invoking the resolver. Gopls owns Go resolution at a UTF-8 byte offset;
TypeScript 7's `tsc --lsp --stdio` owns JavaScript, TypeScript, and JSON resolution; and
`pyright-langserver --stdio` owns Python resolution. The shared LSP client owns one process-scoped
initialize, document-open, query, and cleanup lifecycle with UTF-16 positions. Hsymbol deduplicates
returned rows by canonical path and line and renders canonical targets as absolute paths for an
explicit workspace, otherwise relative to the workspace, before applying the shared verified-row accumulator. It provides the same query's semantic response
as stock metric evidence. Definition expansion reuses inspect_file's language outline projection
and requires the returned definition selection to match an exact supported declared-name token;
every other definition remains one line.

The inspect_file built-in owns bounded structural inspection of one regular file with ordinary
absolute or executor-cwd-relative path resolution; Codex owns permissions. It uses pinned Lezer
parsers for Go, Python, Markdown, JSON, and every stable TypeScript 7 source format. Outline
`line` and `line_end` are shared verified-row identities for the inclusive span and are copyable
HPATCH targets. The projection contains navigation metadata without source bodies.
Unsupported extensions stop after file metadata. The renderer owns the 64 KiB
complete-document budget and explicit truncation; parser recovery remains an independent result flag.

The generated built-in JavaScript and runtime host are materialized inside the authenticated
process snapshot. The directly launched shell child verifies that snapshot before loading an
implementation; configured plugin children retain symlink-based verification. The router never
pre-reads files and never fabricates an `apply_patch` result for
read, search, symbol lookup, or inspection. Model history retains shell calls, private commands stay outside
response routing and edit recovery ancestry. Transport capture observes the resulting call once.
Registry shutdown removes configured frontends and the snapshot.

Library callers pass an already-authorized root and a root-relative cwd. Absolute operands
are matched against the canonical root name; equivalent aliases are not resolved outside the
capability. Translation and commit consume the same identities, so cwd affects relative operands
without changing the workspace boundary.

The caller owns coordination of overlapping filesystem writers from edit-authoring reads
through application and rollback, including create and move destinations. Translation callers
retain that coordination until the host executor completes the patch. Hosts applying private
retained scripts have the same responsibility. Neither an `os.Root` capability nor a rendered
patch provides writer serialization, baseline reservation, or a cross-file snapshot.

The root library validates and formats the state report before an external effect, retains
the report, aliases, patch, and patch summary privately through finalization, and publishes
them only on a successful return. Failure results retain lifecycle and effect metadata,
including a completed application followed by late cancellation, without success projections.
Apply stages
the complete engine result and installs it through ordered filesystem operations; translation
completely renders the patch without mutation. No script command crosses the external commit
boundary. Atomic evaluation does not imply crash-atomic installation or isolation from external
readers. The transaction
coordinator owns backups, ordered operations, rollback attempts, and honest reporting of external
commit or rollback failure.
