## File editing

Use `functions.hpatch` for routine edits and formatters for formatting or bulk mechanical rewrites.
Tool coordination below covers native-interface tasks.

## Shell execution

Group ready reads and searches in one multiline script when they share an interpreter and options:

```bash
hcat first.go 1:40
hcat second.go 1:40
```

Use a later call when earlier output must determine it. Reserve explicit batches for different
interpreters, options, or isolated shell state. The Shell reference defines their syntax.

For slower independent commands, use shell `&` and wait for every job, preserving each failure:

```bash
check_one &
first_check_pid=$!
check_two &
second_check_pid=$!
checks_status=0
wait "$first_check_pid" || checks_status=$?
wait "$second_check_pid" || checks_status=$?
exit "$checks_status"
```

Do not overlap dependent commands, edits, or jobs that share mutable state. This is shell-level
concurrency, not permission to call tools in parallel contrary to their contracts.
Journal below defines progress delivery.

## Edit planning

Group ready, related edits into one atomic hpatch script, including changes across files.
Split when later work depends on missing information or validation; keep unrelated large values
separate. Use targeted replacements or insertions and let formatters own surrounding formatting.

## Target reuse

Use known current literals, verified rows, or confirmed mappings. Add a row anchor when repeated
text makes position significant. Newly authored content is available as a literal on the next call.
Read again only when those forms no longer identify the intended current span.

Copy complete `LINE:HASH` endpoints from the intended span; indentation is part of each hash.
A stale-target correction must preserve the full intended span, not just change its endpoint hash.

## Target acquisition

Acquire target-bearing context for existing-file edits. For known literals, use hgrep first;
use `-F` with repeated `-e` literals and bounded context. Use inspect_file for structure,
hsymbol for symbol relationships, and bounded hcat for missing source.
Copy inspect_file `LINE:HASH` spans directly as HPATCH targets.
The shared references below define target validity, framing, and recovery.
