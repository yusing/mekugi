# Rejected-script recovery

## REQ-CORRECT-001 — Rejected-script recovery

The router exposes a separate model-visible `functions.hpatch_recover` tool with
dedicated syntax. Edit-only recovery is unavailable from public root APIs and
ordinary `functions.hpatch`; it is selected only by the dedicated tool, never by
inspecting an ordinary hpatch payload.

Each rejected-script command has a `C<number>:<hash>` handle with a full SHA-256
binding encoded as 43 unpadded base64url characters. It binds the complete attributable
command frame and the entire retained baseline. This changes only the encoding, not
binding strength, storage, lifetime, or recovery actions. Changing a preceding
path or another command invalidates old handles even if a mutation's bytes survive. The target-only shortcut has one form per line:
`C<number>:<hash> TARGET`. `TARGET` uses
the ordinary HPATCH/2 row, range, anchored-literal, or unanchored-literal target syntax and must
denote a different target from the retained command. This shortcut
has no operation keyword and changes no operation, value, framing, command order,
or file context. Target parsing and script rebuilding preserve the public target literal's
exact decoded bytes, including escaped LF, and enforce the same empty, CR, and control
exclusions.

Alternatively, one recovery payload contains ordinary target-bearing `type`/`add`
mutations against the retained rejected-script text. These use the public row,
range, anchored/unanchored literal, EOF insertion, quoted-value, raw-heredoc, and
line-framed text syntax. File commands and targetless initializers are not allowed.
The two payload forms cannot be mixed.

Script-text mutations may repair operations, paths, values, framing, and command
order, or remove conflicting commands. Their targets address the retained script,
not workspace files. Unrelated prepared text remains byte-identical. The complete
rebuilt script must be nonempty and different from its retained baseline.
The baseline and every planned command result are limited to 1 MiB, checked before
rendering expanded content. A later shrinking edit does not excuse an oversized
intermediate plan.

The router owns recovery grammar, parsing, handle resolution, ancestry, worktree isolation,
dispatch, replay, diagnostics, and reevaluation. Every command handle resolves against the latest
visible evaluated rejected script as one immutable baseline. Each handled command may appear at
most once in a shortcut payload. Both forms rebuild through the root
`EditTextBounded` primitive, then evaluate the complete resulting script normally.
Ordinary and recovered scripts share target-aware
evaluation: workspace targets translate to a host patch, while `@shell/` targets apply
directly inside the current thread’s private retained-script storage. Recovery retains
the reference in its rejected baseline and replay, never resolving it as a workspace path.

A malformed, stale, unchanged, conflicting, incomplete, cross-worktree, or otherwise invalid recovery
changes neither workspace state nor retained rejected ancestry. Proxy-rejected attempts keep
the last evaluated script as the next baseline. A re-rejected recovery becomes the next
baseline, and replay restores the exact `functions.hpatch_recover` payload while retaining its
rebuilt script for later recovery. Non-mekugi plugin and shell failures never enter this
ancestry. Input truncation removes calls the conversation no longer shows from the request's
recovery view, without deleting durable replay records needed by another branch. Resumed and
forked threads inherit only ancestry actually visible in their input. Ordering and executor
confirmation are request-local; concurrent requests cannot alter each other's recovery baseline
or target aliases.

Mixed HPATCH/shell invocations and their resume calls under
[REQ-SCRIPT-001](script.md#retained-continuation) are not edit-only recovery
baselines. If the latest visible HPATCH invocation was mixed or a resume,
`hpatch_recover` directs the agent to inspect checkpoints, current files, and
known sessions, then use `hpatch` with `resume HANDLE` only if continuation
retention succeeded. Mixed preflight failure without a retained handle instead
reports that no segment ran and asks for a corrected script through `hpatch`.
A rejected resume request directs the agent to correct its diagnostic and inspect
the original continuation state, not resend the original mixed script.
It never falls back to an older rejected edit-only script or infers execution
success from preflight or translation. This diagnostic performs
no execution and changes no workspace or retained continuation state.

When every structured rejection is `row-stale`, the routed diagnostic lists only the rejected
target-bearing commands and their current `C...` handles. Recovery guidance directs the model to
submit one `C... TARGET` line per listed command in one atomic payload. Every non-target or mixed
failure instead offers ordinary script-text edits and at most 12 bounded verified
script-row previews around rejected command headers and value locations. Previews
explicitly distinguish script rows from workspace rows; exact known literals can
address other retained text without re-emitting the complete script. A handle from
an older baseline is stale. A re-rejection explicitly states that no workspace file
changed and corrections survive only in the new rejected-script baseline. Earlier
command handles are invalid; script rows must verify against the new baseline. Correlation IDs remain stable and attempt
numbers increase across evaluated and proxy-rejected calls. The transport capturer observes each
provider-emitted `hpatch` or `hpatch_recover` call and its delivered carrier without changing
recovery ancestry.

Outcome hooks report one routed attempt once. Their structured event includes tool identity,
chain and call identity, attempt number, correction marker, lifecycle stage, outcome, and
emitted, evaluated, and translated-patch byte counts. `unevaluated/rejected` means the router
rejected the request before engine evaluation. `evaluated/rejected` means engine evaluation
failed without host mutation; `translated/succeeded` means a host patch was produced but does
not claim Codex applied it; `applied/succeeded` means root-owned application completed, while
`applied/failed` means root-owned commit or cleanup failed. Recovery hook Markdown treats the
exact short recovery payload as model-emitted, renders its target delta or a
script-text correction summary, and identifies the complete script as router-rebuilt.
Routed evaluator rejection invokes
the outcome hook, not a second per-command error hook. Root error hooks remain separate from routed outcome hooks.

Acceptance:

1. `functions.hpatch_recover` has a dedicated grammar and accepts either `C... TARGET`
   shortcut lines or ordinary target-bearing script-text mutations, never a mixture.
2. Every command handle resolves against one immutable latest evaluated rejected script, and a command appears at most once per payload.
3. A successful rebuild is reevaluated as one complete ordinary HPATCH/2 script.
4. Re-rejection advances the baseline, emits refreshed target-command handles, and invalidates every prior handle; proxy rejection leaves the baseline unchanged.
5. Recovery cannot use another request's nonvisible calls or cross selected worktrees, and
   unrelated tools cannot become bases. Resumed and forked threads can recover a visible inherited
   rejection without importing later parent calls. Rejected replay validation changes no ancestry
   or executor confirmation.
6. Replay restores `hpatch_recover` identity and the exact emitted short payload.
7. Ordinary mutation-leading HPATCH scripts are never detected as recovery.
8. Captured provider calls remain individual and correlate to their actual delivered carriers.
9. One shortcut payload corrects multiple distinct command targets without changing
   other fields. A script-text payload can repair several fields, bodies, framing,
   or conflicting commands while preserving every untargeted byte.
10. A target correction can retarget an anchored or unanchored mutation to exact multiline
    text with escaped LF; rebuilding preserves the target bytes and public control exclusions.
11. A target correction that denotes the retained target rejects before root reevaluation,
    including an explicit default occurrence count, an equivalent quoted escape spelling, or a
    range whose two endpoints are the retained single row; the retained baseline and handles remain usable.
12. Script-text edits reject malformed, stale, conflicting, unchanged, empty-result,
    or oversized corrections without advancing ancestry or evaluating workspace
    edits. Accepted reconstructions use the same ordinary dispatch, replay,
    re-rejection, and isolation rules as target-only corrections.
13. After a mixed script completes an edit and then fails a shell or edit segment,
    `hpatch_recover` refuses with mixed-specific remaining-work guidance. The same
    exclusion applies to interrupted, successful, and preflight-rejected mixed
    calls. Test the diagnostic as well as refusal: it must neither claim a failed
    or unresolved call succeeded nor select an older edit-only rejection. A mixed
    preflight failure without a retained handle reports that no segment ran and
    requests corrected input, without advertising `resume HANDLE`.
