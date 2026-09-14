# Shell-routed verified-row reader

## REQ-READ-001 — Shell-routed verified-row reader

In Mekugi mode, the model receives `hpatch` and `shell` as standalone custom
tools. The [agent-guidance contract](guide.md) owns persistent workflow guidance.
Hcat, hgrep, hsymbol, and inspect_file are private commands available only inside
the shell execution boundary: their specifications are not sent as model-visible
tools, direct model calls to their names are not routed, and no executable frontend
is installed for them.

The private `hcat` command accepts exactly one file:

```text
hcat [-n N] [--max-tokens N] [--preview-bytes N] [--tail] PATH [START:END]
```

The shell owns quoting and argument separation. A path containing whitespace is therefore one
ordinary quoted shell argument. `START:END`, when present, is an inclusive logical-line range
whose positive one-based base-ten endpoints must be ordered. The start line must exist. An end
past EOF returns through the final line. One `hcat` invocation never accepts a second path or a
newline-delimited batch. The model batches related reads as separate hcat commands in one
shell script.

The shell boundary resolves the authenticated private commands for the current
thread. These names are not filesystem entries and do not depend on `PATH`.
Deployments with separate router and executor filesystems must make the authenticated
runtime available at the same absolute location on both sides.

Hcat runs in the shell carrier's actual working directory. Relative and absolute paths keep
their ordinary process meaning. Codex, not the router or hcat, owns sandbox and filesystem
permissions. The worker accepts only regular UTF-8 files and never mutates them. It emits only
the requested logical lines:

```text
LINE:HASH TEXT
```

`LINE` is the positive one-based logical line number. `TEXT` is exact logical-line content
without its terminator. `HASH` is lowercase hexadecimal for the first two bytes of SHA-256
over that exact content, including leading spaces and tabs. A trailing file terminator does
not create an additional empty line. Missing, inaccessible, non-regular, non-UTF-8,
reversed-range, and start-past-EOF reads return concise stderr and nonzero status.

Verified-row commands count exact formatted current stdout with the GPT-5 tokenizer. They admit
rows through 15,000 tokens. One next complete row may raise the result to at most 15,500 tokens;
admitting that row seals the result. EOF at that point is complete. A later row, or any row that
would exceed 15,500 tokens, is omitted together with every later row. Omission preserves already
admitted complete rows on stdout, writes an incomplete-result diagnostic to stderr, and returns
nonzero. It never cuts a row.

For a truncated prefix read, hcat also reports the inclusive unread row range,
starting at the first row not emitted and ending at the requested end or EOF,
whichever comes first. This range refers to the file just inspected, not a durable
snapshot or a continuation handle. A subsequent bounded range read can recover the
missing rows without repeating the prefix. If no complete row fits a token budget,
the diagnostic suggests preview mode. Source-bound rows instead require a byte-window
reader. Tail reads do not report a prefix-resumption range.

Hcat and hgrep accept the same optional leading `--max-tokens N` and
`--preview-bytes N` pairs, in either order, each at most once. Flags must precede
the path or ripgrep arguments. Their values are positive decimal integers.
`--max-tokens` accepts 1 through 15,500 and sets a strict GPT-5 stdout token ceiling:
there is no whole-row overshoot in this mode. Without it, the default admission
rule above is unchanged. Missing, repeated, or out-of-range values reject before
reading source content or starting ripgrep. Outer host output budgets remain independent; callers
can choose a reader ceiling that fits the enclosing result budget.

`--preview-bytes` accepts 1 through 65,536 and changes each stdout row to a JSON
record with `row` (the complete source's verified `LINE:HASH`), `preview` (a UTF-8
prefix no larger than N bytes), `source_bytes`, and `omitted_bytes`. Hgrep also
includes `path`. This is an explicit inspection format, not exact source-row text.
The row reference remains usable as a whole-row target; preview text must not be
treated as a complete literal replacement or match. Hashing still covers every
source byte, never just the prefix. A prefix may end before N to avoid splitting
a Unicode character. Preview records themselves count against the same stdout
token budget. Preview byte omissions are intentional and counted in each record;
omitting an entire record at the token ceiling is still incomplete and nonzero.

Exact token counting must remain practical for long unbroken words and whitespace up to
the bounded candidate size. The pinned model's token identities and splitting rules
remain unchanged; large pieces must not require quadratic repeated merge scans.

Hcat additionally accepts leading `-n N` and `--tail`, each at most once. Line counts
are canonical positive safe integers. `-n` selects first/last N complete logical source
lines within the requested range, before any explicit token ceiling. Without a token
ceiling, line mode bypasses tokenization and the default token admission rule; selected
exact line lengths determine storage. Omitted lines retain the incomplete/nonzero contract.
`--tail` requires `-n` or `--max-tokens`. It selects a suffix of complete formatted rows
from the file or requested range, in original source order, within any supplied limits. It never cuts a row or
skips an oversized final row to show earlier content. Preview mode still takes each
selected source row's prefix. Omitted earlier rows use the existing incomplete/nonzero
contract; a complete suffix covering every selected row is successful. Empty files succeed.
Tail reads scan and validate the whole file, including source beyond the requested range. A source-bound row clears earlier tail candidates;
later verifiable rows may still be retained. Hgrep does not accept this option.

Hcat retains its bounded whole-row candidate storage in token-limited and preview modes. A source
row exceeding 1,984,000 UTF-8 bytes cannot be verified by this reader; it is omitted
with a distinct source-bound diagnostic and nonzero status. Use a byte-window
reader when such a file needs content inspection. Whole-file UTF-8 validation
still runs even after stdout admission stops. Retained `@shell/` reads accept the
same options through the existing thread-private descriptor path.

Acceptance:

1. A whole-file or bounded read emits exact UTF-8 rows. Equal lines at different positions
   have distinct row references, and indentation changes the hash.
2. `hcat PATH`, `hcat PATH START:END`, and a shell-quoted path containing whitespace work.
   Extra path or range arguments fail instead of being interpreted as a batch.
3. Several hcat commands in one shell call execute in authored shell order without an
   hcat-owned batch format, buffer, header, or partial-success policy.
4. Reading and whole-file UTF-8 validation use bounded streaming storage and observe
   cancellation. Token-limited output retains only admitted complete rows without a second read.
5. Success and failure reach Codex through the model-visible shell carrier. Replay retains
   the original shell call and output; it never synthesizes a model-visible hcat call or
   includes the shell call in editable rejected-script recovery history.
6. Router startup validates hcat inside the immutable built-in snapshot without installing a
   frontend. Passthrough mode loads and exposes none of these replacement surfaces.

7. Hcat and hgrep enforce a caller's strict token ceiling identically, including
   complete-record admission, preserved prefixes, and explicit nonzero incompleteness.
8. Preview records retain exact full-source identities, bounded UTF-8 prefixes,
   and byte omission counts for long rows. Default exact output is unchanged.
9. Invalid and duplicate options reject before source content is read. Retained
   path resolution can precede option validation. Quoted paths, line ranges,
   and thread-private retained reads work with either leading option order.
10. Tail selection works with either option order, quoted paths, ranges, previews,
    and retained descriptors. Missing limits and repeated `--tail` reject before reading.
    In token-limited mode, long and source-bound rows cannot cause unbounded storage or prevent retaining later
    rows; invalid UTF-8 anywhere in the file still fails.
