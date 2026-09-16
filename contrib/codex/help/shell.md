# Advanced shell options

These options are for separate execution contexts, program input, or retained source.
Ordinary commands need none of them.

## Interpreter selection

Selectors named `bash` or ending in `/bash` use the embedded Bash evaluator;
`sh` or a path ending in `/sh` selects its POSIX evaluator. Other interpreters have
no private reader or journal builtins.

### Execution options and program input

The input order is: optional interpreter selector, optional directive lines, then program source.
Without a selector, the body is Bash. Put directives together before the body; `#!cmd=` and
`#!params=` may appear in either order, at most once each per program.

- `#!params=<JSON object>` supplies the request-specific execution fields listed in the tool
  description. The body supplies `cmd`, so omit that field; if setting `login`, use `false`.
  Omit `workdir` to use the current workspace. A necessary override must be a fully expanded
  existing absolute path, never a reference or placeholder.
- `#!cmd=` accepts exactly one `{.}` placeholder, which expands to the script runner invocation.
  Use it to connect a producer to the program's standard input, independently of its source body.

Example: pipe data into a Python body:

```python
#!python3
#!cmd=curl -fsS https://example.com/data.json | {.}

import json
import sys
print(len(json.load(sys.stdin)))
```

### Batching

Explicit batches require Code Mode and run sequentially, with separate shell state per program.
Results arrive together after completion.

Start a batch with `#!batch=SEPARATOR`, choosing a nonempty separator line absent
from every program's source and without surrounding whitespace. Put that exact line
between programs, with no leading or closing separator. At least two programs need
nonempty bodies. Each program has its own optional interpreter and directive block;
a params-only header selects Bash.

```text
#!batch=NEXT_PROGRAM
#!params={"yield_time_ms":1000}
echo hello
NEXT_PROGRAM
#!python3
print("hello")
```

Omitted params inherit the previous complete object; an explicit object replaces it, and `{}`
clears it. Interpreters and command templates never inherit. Each program starts a separate
execution, so shell variables and `cd` changes do not carry over. Only the chosen separator
line is reserved; without a batch header, selector-like body lines stay native source.

`#!batch=SEPARATOR` continues after nonzero exits; `#!batch-stop=SEPARATOR` leaves later programs unstarted
after a nonzero terminal exit. Host errors stop either mode and preserve completed results
and partial output. Use separate shell calls for interactive programs.
Native-only clients reject batches; submit separate calls there.

### Bounded command output

Hrun bounds noisy external-command output for display. Use
`hrun [-n N] [--max-tokens N] [--tail] -- COMMAND [ARG...]` when that output needs a limit;
run commands directly when output is quiet or must remain complete, including output saved to a file.
Supply at least one limit; token limits are 1–15500, shared stderr-first.
`-n` selects complete lines before token limiting; alone it skips tokenization.
`--tail` keeps the ending. Hrun preserves the command's exit status and waits for
completion; infinite producers require cancellation. Use an explicit shell for compound commands.

## Retained program source

Retained scripts are thread-private and expire at the reported deadline or earlier on
router shutdown. Reads and edits do not renew them. Save durable source in workspace files.

A retained result includes `retained: true` and a `script_ref`. Read the source with
`hcat @shell/<reference>`, edit it with hpatch, or rerun its current content with a shell call
containing only `#!script=@shell/<reference>`. A HPATCH script using an `@shell/` path must use
only `@shell/` paths; never mix retained scripts and workspace files in one HPATCH script.
