# HPATCH/2 script grammar

## REQ-SCRIPT-001 — HPATCH/2 script grammar

Outside a multiline value body, blank lines are ignored and every other physical line begins exactly
one command:

```text
type TARGET VALUE
add TARGET VALUE
append VALUE
```

Filenames are outer invocation arguments, never part of the edit script. Each script is bound
to its invocation-supplied file. There is no active-file selection or file-management command.
Create, move, and remove files through the outer shell.

Targets are:

```text
ROW                         complete logical line
ROW..ROW                    inclusive complete-line range
ROW "TEXT" [COUNT]          anchored exact literal occurrence(s)
"TEXT" [COUNT]              whole-baseline exact literal occurrence(s)

ROW   := LINE:HASH
LINE  := positive one-based decimal logical line
HASH  := exactly four lowercase hexadecimal digits
COUNT := positive decimal integer; default 1
```

An add target is a single `ROW` or an anchored or unanchored text target. `add` does not
accept a range. `append` inserts once at the immutable baseline end, requires no target,
and contributes no target metric. `EOF` is not a keyword.

No whitespace is permitted inside `ROW..ROW`. A line target owns the complete logical
line, including its terminator when one exists. A range owns all
complete logical lines between its endpoints, inclusively.

A text target either verifies its anchor row and starts at that row's column 1 or, without a
row, starts at byte zero. It searches exact literal content forward through EOF. `TEXT` is
nonempty. Its quoted source remains on one physical command line, but JSON-escaped LF (`\n`
or an equivalent `\u000A` escape) decodes into the exact target literal and may make one match
span logical lines or include a trailing LF. Literal horizontal tab is also accepted. Raw
physical newlines, CR in every representation, and every other C0 control are forbidden.
Matching is left-to-right and resumes after each complete match. The target contains the first
`COUNT` non-overlapping matches and rejects if fewer exist.

`VALUE` is a JSON-compatible quoted string or a heredoc.
Inline strings decode JSON escapes and Unicode escapes and additionally accept literal
horizontal tabs. Quotes, backslashes, line terminators, NUL, and other C0 controls remain
escaped.

A heredoc uses a caller-chosen delimiter, optionally single- or double-quoted:

```text
type 12:a1b2..15:c3d4 <<'END'
replacement
text
END
```

The closing line must equal the delimiter after quote removal. `<<-END` strips leading
tabs from body and closing lines, as in shell heredocs; it does not remove the final newline.
Spaces are not stripped. Delimiters are nonempty shell words parsed by `mvdan/sh`;
quoted parts and backslash quoting may be combined. Quote spaces and shell metacharacters.
Whitespace between `<<` (or `<<-`) and the delimiter is accepted.

Bodies are literal UTF-8: no interpolation or escape processing occurs, regardless of
delimiter quoting. Every body line retains its physical LF/CRLF terminator. Bars and
command-shaped lines are ordinary data. Choose a different delimiter when the body
contains the closing line. There are no reserved PATCH/TEXT modes or newline-chomping
suffixes. Use a quoted string when the value must omit its final newline.

The header, body, and delimiter form one command attributed to the header. Missing closes,
invalid UTF-8, and bodies over 1 MiB reject the entire script before mutation. The limit
applies to decoded bytes after optional tab stripping. Existing transport limits still apply.
An unterminated frame owns the remaining input, so payload-shaped commands are not executed.
The shared parser checks that the closing delimiter matches the header before any effects.
Complete heredocs permit end-of-input with or without trailing line endings and blank lines.
The same framing rules apply to recovery.

Whole-line replacement still applies the terminator-preservation rule in `REQ-EDIT-001`.
Literal targets own only their exact matched bytes; heredocs do not consume adjacent
baseline whitespace.

The grammar is unambiguous by operand shape. For example:

```text
type 12:a1b2 "line replacement"
add 37:8c2f "// parseCommand parses one physical script line.\n"
type 12:a1b2 "needle" "replacement"
type 12:a1b2 "needle" 3 "replacement"
type "known current text" "replacement"
append <<PATCH
appended text
PATCH
```

Paths are nonempty invocation arguments, decoded by the shell, not the edit parser.
Root-scoped library application
through `Apply` or `ApplyForHost` resolves relative paths from cwd; absolute paths must remain beneath
the canonical root, and lexical or symlink escapes fail. Host translation through
`TranslateForHostAt` and `ApplyForHostAt` instead use an optional host directory without filesystem
confinement. With a directory, relative operands resolve from it; without one, relative operands
reject and absolute operands remain valid. Router process cwd is never an implicit base. Emitted
patch paths retain cleaned host identities for Codex to authorize.
Trailing operands, malformed rows, forbidden controls, missing values, and unknown
commands are invalid.

Acceptance:

1. Every edit command is `type`, `add`, or `append`, without a filename.
   The removed `in`, `new`, `mv`, `rm`, targetless `type`, and `add EOF` forms reject.
2. Line, range, anchored text, and unanchored text targets parse without a separate selection command, and inline
   replacement values remain distinguishable from a text target's quoted literal.
3. Anchored and unanchored text targets accept JSON-escaped LF and exact multiline or
   trailing-LF matches while raw physical newlines, CR, empty literals, and other forbidden
   controls reject.
4. JSON-compatible values and heredocs reproduce their decoded payloads without parsing
   body lines as commands. Caller-chosen and quoted delimiters, tab-stripping heredocs,
   literal bars, command-shaped payload, and LF/CRLF terminators work for every target shape.
   A missing or mismatched closing delimiter rejects atomically.
5. Invalid rows, ranges, counts, strings, heredocs, operands, and commands fail before
   filesystem mutation, patch output, or final-state reporting.
6. One invocation may supply scripts for multiple paths while all targets retain the immutable
   baseline meaning defined by `REQ-SELECT-001`; the entire multi-file invocation is atomic.
7. For root-scoped evaluation with root `/workspace` and cwd `bin/worktree`, path `main.go` denotes `/workspace/bin/worktree/main.go` and translates as `bin/worktree/main.go`.

### Shell invocation

The model sends edits through `functions.shell` as `hpatch PATH SCRIPT`, or uses
`hpatch PATH` to supply the script on stdin. Multiple `PATH SCRIPT` pairs in one call
form one atomic multi-file edit. Paths are ordinary shell arguments, so shell quoting
handles spaces without adding filename syntax to HPATCH. `--` ends option parsing for
literal flag-like paths. `hpatch` may appear anywhere an ordinary command is valid, including
conditionals, lists, pipelines, and subshells. A column-zero `#!bash` starts a separate sequential
Bash program with isolated shell state; it is not required around `hpatch`. The existing shell owns
inline environment assignments, argument expansion, quoting, heredoc expansion, substitutions,
stdin, redirection, and exit-status flow.

The HPATCH parser receives the resulting bytes and accepts only edit commands.
There are no `shell` or `resume` commands inside an edit script. The host-authorized
shell worker invokes `ApplyForHostAt` directly, using the execution directory.
Codex owns permissions, sandboxing, cancellation, and native session continuation.

Evaluation failure changes no edit targets. Shell expansions and redirections happen
before evaluation and retain ordinary shell effects. Application stages all changes
and attempts rollback on failure as specified in [REQ-OUTPUT-001](output.md).

Acceptance:

1. Argument and stdin invocation produce the same edit for the same path and decoded bytes.
   Multiple path/script pairs validate together before any edit is applied.
2. Unquoted shell heredocs expand substitutions normally; quoted heredocs preserve them.
3. Input and output redirection use the shell's ordinary streams.
4. Composed `hpatch` commands retain normal shell control flow and stream semantics, including
   conditionals, lists, pipelines, subshells, command substitutions, and background jobs.
   Inline environment assignments (`ENV=VALUE ... hpatch`) remain supported. A `#!bash` separator
   creates another batch program rather than changing `hpatch` semantics.
5. Both native and Code Mode carriers run the same shell worker without a mixed-script runner.
6. Replaying a tool result never executes the edit again.
