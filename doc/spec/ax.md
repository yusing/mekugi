# Agent-experience evidence

## REQ-AX-001 — Runtime reads and evidence-backed AX reporting

Authenticated `mcat`, `msymbol`, and `inspect_file` frontends observe their
actual stock-executor invocations. An absolute `MEKUGI_AX_OUTPUT` opts into a
local `mekugi.ax.read.v2` JSONL journal. `--debug` creates that journal unless
an explicit path is supplied, pins it in the authenticated worker manifest,
and includes an automatic AX report. No static source scan or router request
count substitutes for executed-read evidence.

Each invocation records a random identity, thread ID, reader name, UTC start
and finish, monotonic duration, success, an allowlisted failure class on
failure, and an optional observed exit code. Direct frontends do not invent a
logical shell-call identity. Missing thread identity stays unattributed.
Loops count each actual invocation; skipped branches and literal examples
count none. Other programs' internal reads are outside coverage. Observation
failure cannot repeat, stop, or change the command result.

The journal is private (`0600`), regular, append-only, and bounded to 64 MiB.
A cross-process lock bounds concurrent appends. Symlinks and nonregular paths
reject; no parent directory is created. Events retain no paths, scripts,
arguments, credentials, source text, or command output. A missing finish is
incomplete; absent or unattributed evidence is unavailable, not zero.

`mekugi inspect-session --session PATH --ax` adds whole-rollout AX data,
independent of call pagination. `--read-log PATH` and `--defects PATH` imply
`--ax`. The offline report includes:

- **Edit observations:** original observed stock patch input bytes, matched
  completed calls, failed results, unconfirmed calls, and exact physical-line
  bytes repeated from the preceding observed patch, with multiplicity capped
  by that preceding input. This is measured re-emission, not a judgment that
  the repeated text was unnecessary.
- **Read observations:** starts and finishes paired by invocation identity,
  per-reader success/failure and duration, allowlisted failure classes,
  incomplete pairs, and explicit other-thread or anonymous exclusions. The
  entire journal is validated before attribution.
- **Completion observations:** paired rollout start/complete events and their
  recorded intervals. Missing or cross-family events are not guessed.
- **Command observations:** recorded command start/completion identities,
  endpoints, exit status, and measured duration. Invalid or conflicting
  timestamps cannot establish a complete interval or certain gap.
- **Defect assessments:** optional explicit `defect` or `no_defect` verdicts
  for known edit call IDs, each backed by a real bounded evidence artifact.
  The report records artifact path and SHA-256, not its contents. A verdict
  is supplied judgment, not proven causality.

Assessment input is bounded to 1 MiB; a journal event is at most 4096 bytes,
and the report admits at most 100000 invocation identities. Invalid supplied
sources fail before producing a partial result. Inspection writes no session,
journal, evidence artifact, or replay record. AX evidence remains separate from
sanitized transport metrics.

Acceptance:

1. Executed reads, loops, skipped branches, failures, and missing evidence
   retain distinct counts without changing command stdout, stderr, or exit.
2. Concurrent writers, malformed journals, incomplete events, and unrelated
   threads cannot create false success or attribution.
3. Stock patch re-emission and failure counts use observed input and durable
   result, not a translated carrier or inferred application.
4. Completion and command timing use paired recorded events, not wall-clock
   guesses.
5. Defect verdicts require known calls and bounded real artifacts; invalid
   assessments fail before a report is emitted.
