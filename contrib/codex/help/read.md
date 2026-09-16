# Reader details

## Retained output

Empty streams are omitted. Stdout-only pages are unframed; stderr and mixed pages use
`[stream unit]` / `[/stream]` frames. Use `--stdout` or `--stderr` on an initial reference
to select one. `--max-tokens N` defaults to 4,000. Later references already bind the
selection and position, so no `--cursor` or repeated producer arguments are needed.
Pages contain raw bytes, complete verified rows, or JSON entries. Do not treat raw byte
fragments or framing as verified source rows. If a complete unit cannot fit, increase
the budget or use a source preview. Reads are repeatable, not consuming, and survive
router restart while their session data is retained. Mekugi cleans inactive session data after
14 days and reclaims the oldest inactive sessions under storage pressure; active work and shared
references stay protected. Missing references fail explicitly. No standalone output
dumps are created. Commands keep their original exit status; `script_ref` stores executable
program source separately. Reading output never reruns the producer.

## File batches and ranges

For several known files, use `hcat --batch [--max-tokens N] PATH [START:END] -- PATH [START:END] ...`
inside shell, with `--batch` first. It divides one display budget across up to 16 hcat
reads and puts a per-file shown/omitted range manifest first. Follow that file's `next_call` to read its retained
omissions, rather than rereading overlapping source ranges. Increase the budget or select
fewer paths if the manifest cannot fit; outer host output limits still apply.

For one file, use `hcat PATH [START:END]`. Quote paths with shell syntax. A bare path
reads the complete file. A start line of `0` begins at line 1 without emitting line 0. An end past EOF warns after
returning available rows; a start past EOF fails. Copy a current `LINE:HASH` directly into an
HPATCH/2 target. Recover omitted rows with its exact `next_call: hread REF`, without
repeating the prefix or reopening the source.

## Preview and selection options

Hcat, hgrep, hsymbol, and inspect_file accept `--max-tokens N` (1–15500, default 4000)
for a strict stdout token ceiling. Options may surround operands and stop at `--`.
Hcat and hgrep also accept `--preview-bytes N` (1–65536) for long-line inspection.
Preview JSON includes a full-source row identity and an explicit UTF-8 prefix with
omitted-byte counts. Retain the identity, but obtain missing content before using the
preview as literal target text. Budget omissions still report incomplete results.

`hcat [-n N] [--tail]` selects first/last complete rows; `-n` alone skips tokenization.
Tail requires a line or token limit. Omitted rows are incomplete; use native selection,
not `| tail`, to retrieve an ending that a budgeted reader would otherwise omit.

## Semantic selectors

For Go, JavaScript, TypeScript, JSON, and Python, use
`hsymbol refs PATH LINE SYMBOL [N]` for semantic references or
`hsymbol def PATH LINE SYMBOL [N]` for definitions. Supply an already-known
`LINE:HASH` instead of `LINE` to enforce a prior read; plain lines query the
current snapshot without requiring a preliminary verified read.
`--workspace ROOT` chooses resolver scope and relative input paths;
its result paths are absolute. Other results are workspace-relative. `N` counts exact
language tokens on the selected line and may be omitted only when one exists. Copy emitted `"PATH":LINE:HASH TEXT` rows directly
into HPATCH/2 targets. Do not follow a complete hsymbol definition with hcat of the same span
unless non-declaration context is needed.
