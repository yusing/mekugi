## File editing

Use `hpatch` through `functions.shell` for routine edits and formatters for formatting or bulk mechanical rewrites.
Tool coordination below covers native-interface tasks.

## Shell execution

Group ready reads and searches in one multiline script. Use shell `&` for slower independent work
and wait for every job, preserving failures. Keep dependent operations sequential.
Reserve explicit batches for different interpreters, options, or isolated shell state.
The shared Shell and Journal references define execution and progress delivery.

## Edit planning

Group ready, related edits into one hpatch invocation. Split only when missing information
or a validation result must determine the next edit; keep unrelated large values separate.
Use targeted replacements or insertions and let formatters own surrounding formatting.

## Target reuse

Use known current literals, verified rows, or confirmed mappings; anchor repeated text when
position matters. Newly authored text is available as a literal on the next call.
Reuse current target evidence instead of rereading solely to reacquire targets.

## Target acquisition

Use hgrep with `-F` and repeated `-e` literals for known targets, inspect_file for structure,
hsymbol for symbol relationships, and bounded hcat for missing source. Copy emitted
`LINE:HASH` identities directly. The shared references define validity, framing, and recovery.
