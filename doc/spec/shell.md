# Installable free-form script tool

## REQ-SHELL-001 — Installable free-form script tool

Mekugi mode exposes one mandatory, unconstrained custom tool named `shell`.
It accepts a bounded UTF-8 program and translates successful input through the
canonical executor carrier from `REQ-PLUGIN-001`. Configured plugins cannot replace
the built-in tool.

Each column-zero interpreter header after the first source line starts a new program,
unless it is inside an incomplete shell construct such as a heredoc or quoted string.
The first program may omit its interpreter and default to Bash; later Bash programs use
`#!bash`. Headers remain part of their program, with every source byte preserved.
No batch directive or separate delimiter is needed. `---` is ordinary program source.

Boundary recognition uses the same interpreter-versus-directive classification as header
parsing: `#!params` and unsupported `#!key=value` directives do not start
programs. Bash, POSIX shell (including Dash and Ash), Korn shell, and Zsh parsing preserves interpreter-like lines inside
heredocs, quoted strings, and other incomplete shell constructs. Indented headers remain
body data. Every program in a batch must have a nonempty body;
consecutive headers and a final header without a body reject before execution.

Batches continue after nonzero exits and stop on host errors/refusals. Each live native
session reaches its terminal result before the next program starts; a yield is not failure
and never triggers another execution.

Each program has its own optional interpreter selector and leading directive block.
A params-only header selects default Bash. Duplicate params within one block still reject.
Interpreter selectors start programs; params and unsupported directives do not.

Omitted params inherit the preceding complete object. A supplied object replaces that object,
including `{}` clearing inherited fields. Interpreters do not inherit.
All programs are parsed and translated before any carrier is emitted; invalid later programs
reject the entire batch without running its valid prefix. Batch programs require a nonempty
body.

Batches require Code Mode and are for noninteractive work. Agent guidance defaults to one
multiline script for ready commands sharing an interpreter and execution options, not one
batch program per command. Slower independent commands may use shell background jobs and
wait for every result when shared output and state permit concurrency. Explicit batches are
reserved for separate interpreters, execution options, or isolated shell state, and preferred
over separate calls for noninteractive programs. They remain sequential, with a combined
result after completion. Interactive programs use separate calls
so their prompts and native continuation handles remain available for input. The router prepares
separate native exec arguments for each program
before sending one ordered Code Mode carrier to Codex. Each native execution receives its own
params and separate shell state. The carrier awaits terminal native results, using the existing
continuation operation when needed, before starting the next program. Nonzero script exits do not stop later programs. The result contains an ordered `results` array, each element preserving
one program's terminal native fields and concatenated output. A host error or refusal stops
remaining execution and propagates after publishing completed results and current partial output,
including any outstanding native continuation handle. No program is restarted or retried.
Every emitted batch envelope includes `batch` metadata: `on_nonzero_exit`
(`continue`), `program_count`, `started_programs`, `not_started_programs`,
and `stopped_reason` (`host_error` or null). Counts describe programs,
not native polling calls. A host-error result can include an unfinished last started
program and its existing native handle. No unstarted program is fabricated as a
completed result or automatically retried. Resubmitting a batch starts new execution.

Native-only clients reject batches with a Code Mode requirement diagnostic before execution.
Replay restores the original call without starting any program.
Eligible cat-write projection applies independently within each program.

The following interpreter and directive rules apply separately to each program.

The first logical line is a shebang when its trimmed form starts with `#!`.
Its remaining whitespace-separated fields select the interpreter and arguments.
A leading `env` or `/usr/bin/env`, with optional `-S`, is normalized away.
Selectors whose basename is Bash or sh use the corresponding bundled shell
semantics; every other bare selector resolves through inherited `PATH`.
Empty, NUL-containing, or oversized selectors reject. Without a shebang, Bash
is selected.

When a shebang is present, the script body is every input byte after the complete first-line
terminator. The tool removes only the shebang line and its terminator. It preserves all leading
and trailing body whitespace, including an absent or final line terminator. Without a shebang,
the complete input is the body. The translated argv contains each normalized interpreter field
followed by the exact body as its final value. The resulting Codex exec carrier therefore shows
`shell python3 <quoted-body>` on one physical command line; the model does not author that command
or its quoting. For implicit default Bash, a body with at most one final line
terminator remains direct when it parses as one non-background, non-negated simple call whose
static command is neither a shell built-in, the reserved `journal` command, nor a private
contribution or an optional command-routing candidate, and whose statement contains no command or process substitution. The direct carrier
removes that optional final line terminator and otherwise preserves the command text.

The `shell` transformation MUST NOT add flags to the rendered command. Only interpreter
arguments supplied by the input's selector may appear as interpreter flags. Router-owned
metadata, including commentary connection details and credentials, must travel through private
runtime plumbing, never through added command arguments or inline environment assignments.
Call-scoped journal credentials and AX identity travel as private framing inside the existing
quoted source argument, after the script body so host command previews show the actual program
first. Historical leading framing remains readable for retained carriers and escapes scripts
that themselves start with its reserved marker. The worker removes that framing before parsing
headers or executing any body bytes. Translated commands add neither environment prefixes nor
outer AX comments.
Enabling commentary must not change whether an otherwise eligible command remains direct.
Journal authoring and reserved argv syntax follow [REQ-JOURNAL-001](journal.md). Shell commentary is thread-scoped. The worker discovers its private publisher through the
inherited `CODEX_THREAD_ID` and current thread runtime, without changing the interpreter argv.
Unavailable journal discovery does not affect scripts without journal commands. An explicit journal command fails if it cannot record its mutation.

After an optional interpreter shebang, a leading directive block can contain one `#!params=`
assignment. The tool trims ASCII spaces and tabs around each complete directive line.
The params value is a JSON object that cannot contain `cmd` because the script body supplies
`cmd`. A present `login` value must be exactly `false`. Within the leading directive block, the
tool tolerates `# !params JSON` and `#!params JSON` as alternate spellings and applies the same
params validation. A duplicate directive, malformed JSON, non-object JSON, unsupported
leading directive, params object containing `cmd`, or unsafe `login` value rejects.

Header parsing and params-policy errors identify the one-based line within the submitted program. CRLF
counts as one terminator. Batch rejection also identifies the one-based program number;
no valid prefix runs when a later header is invalid.

The tool removes recognized directive lines and their complete line terminators from the body.
Without an interpreter shebang, the worker selects `bash`. An eligible simple external
Bash command remains direct, including when exec parameters are supplied; every other body uses
the worker command as the complete outer command. Use ordinary shell pipelines and redirections
for compound programs. After the first body line, directive-like lines remain ordinary body data.

The worker carrier starts the fixed helper once with the normalized interpreter
fields and quoted source, including its existing header. The worker uses the shared header
parser to recover the exact body before passing it to the selected interpreter. An argument-free helper invocation is a private control
channel, not a script-input interface; unsupported stdin returns an actionable
diagnostic.

Bash and POSIX programs execute with their selected semantics inside the authenticated
worker. Private read, search, symbol, inspection, and bounded-command operations share
that execution's cwd, environment, redirections, pipelines, output, status, and
cancellation. Other commands and interpreters use the inherited executor environment.
Cancellation and output limits apply to descendant processes through a bounded
foreground-process and inherited-pipe shutdown, without starting another router
worker or preventing direct commands and pipelines from using terminal input.

Bash/POSIX workers forward bounded, valid UTF-8 stdout and stderr to Codex as it
becomes available, including completed reads before later commands finish. Forwarded
bytes are not repeated at completion. Redirections and pipelines keep their shell
meaning; only output reaching the worker's outer streams is forwarded. Codex still
owns yield timing, delivery, continuation, and cancellation. A later failure cannot
retract an already delivered prefix; malformed UTF-8 and overflow still fail explicitly.
Other interpreter plugins remain completion-buffered.

Workers read the display budget from the existing script header, defaulting to 10,000 tokens.
The quoted program carries that header as data; no budget flags or environment variables are
added to the translated command. The shared parser removes headers before execution, preserving
the exact authored body. One balance covers both outer streams and every command in that
worker. Selection uses the shared tokenizer, accounts for nested JSON escaping, reserves up to
1,024 tokens, bounded by the available allowance, for diagnostics and native framing, and does not cut
verified rows at a display boundary. Small writes may use escaped byte length as a conservative
token bound. Ready output continues streaming until this display budget is spent. Redirections
and pipelines are not display streams and keep their original bytes.

Display exhaustion does not stop execution or change command status. The worker retains
only omitted stdout/stderr through [managed read continuation](read.md#managed-read-continuation).
The shared `next_call: hread REF` receipt replaces standalone output dumps. Existing producer
byte limits, invalid UTF-8, cancellation, and execution failures still apply; retention
cannot make an incomplete producer complete.
The receipt itself needs display space: a caller budget smaller than the receipt and host
framing can still truncate it. Execution is not rejected or assigned a larger host budget
to hide this limitation.

Explicit batches divide an 8,000-token allowance across their programs, also respecting smaller
per-program budgets. Split cat-write carriers divide their allowance among steps and
fall back to one worker when a split would leave too little display space. Native request parameters remain unchanged. Direct single external-command
carriers keep Codex's output behavior. Shell pipelines and redirections receive complete
program output before display retention.

### Optional command routing

The built-in RTK policy plugin transforms expanded command arguments, not shell source.
It does not execute commands. Its candidate names and mappings are snapshotted with the
registry; no RTK command mappings belong in the Go runner. Candidate commands use the
authenticated worker so routing observes the executor's current cwd and PATH. When `rtk`
is not executable there, commands run unchanged.

Only recognized command forms are routed. Unsupported subcommands, explicit executable
paths, already-routed commands, private readers, and explicit machine-readable output
remain unchanged. Automatic routing applies only to display output: commands whose
input or output is redirected, piped, or captured by substitution remain raw.
Non-Bash/POSIX interpreters are not automatically routed. An explicit
`hrun` opts its inner external command into routing before output capture and selection.
Terminal-backed commands and interactive Git staging bypass automatic routing. Native `find`,
`diff`, `git add`, and `pnpm typecheck` also remain raw because their RTK
counterparts change more than display output.
RTK changes the displayed output; the existing command executor retains ownership of
argv, environment, exit status, cancellation, and descendant cleanup. Rewrite failure
falls back to the original argv before execution, never by retrying an executed command.

The shell-owned command `hrun [-n N] [--max-tokens N] [--tail] -- COMMAND [ARG...]`
executes one external command with selected displayed output. At least one limit is required.
Token budgets are canonical positive decimal integers from 1 through 15,500; line counts
are canonical positive integers fitting a Go int. Each option may appear once, in any order;
`--` and a nonempty command are required. Invalid arguments reject with status 2 before execution.
Hrun has no plugin contribution, installed frontend, or model-visible custom tool. Simple
hrun calls must use the worker, not the direct external-command carrier. Its name is reserved
against configured plugin declarations. It does not add private-reader AX events.

Hrun uses the existing external-command owner for PATH resolution, argv, current directory,
exported environment, stdin, signals, cancellation, and descendant cleanup. It does not
evaluate shell syntax, shell functions, or private reader names. An explicit shell executable
is required for compound commands. Display-budget exhaustion never cancels the command:
stdout and stderr are drained to completion. Token-only capture uses byte-bounded buffers;
line capture retains at most N complete lines per stream plus the current unfinished line,
so line-only memory depends on line lengths. With both limits, each line candidate is
byte-bounded during ingestion before final token selection. Infinite producers still require cancellation.
Prefix mode keeps the beginning of each stream; tail mode keeps the ending. Results are delivered after completion,
not streamed as live progress. Existing host continuation and cancellation remain authoritative.

`-n N` selects the first or last N LF-delimited lines per stream, preserving terminators
and counting a nonempty unterminated final line. It never cuts a selected line. Without
`--max-tokens`, no tokenization runs. With both limits, line selection precedes token
selection; that subsequent token ceiling may cut a line. Tail selection waits for EOF.

The strict GPT-5 token budget is shared by retained command stdout and stderr, counted
independently, with stderr allocated first and stdout receiving the remainder. Streams stay
separate. Selection may cut lines but not UTF-8 characters; malformed byte sequences are
rendered as replacement characters. Omission adds a fixed `hrun: output incomplete` diagnostic
on stderr outside the command-output budget. Omission alone preserves the command's actual
exit status, including success, nonzero exit, and signal status. Missing executables retain
status 127. Closed downstream pipes from shell-owned commands become command status 141,
not fatal interpreter errors: ordinary pipelines use the last stage's status, `pipefail`
observes the failure, and subsequent statements run unless shell error policy stops them.
Other execution, capture, and output-write errors propagate normally. Outer shell and
host output limits remain independent.

Other interpreters retain the plugin executor path. It passes middle fields as interpreter
arguments, supplies the final exact body through an anonymous script descriptor such as
`/dev/fd/3`, and leaves standard input available as program data. Neither path stores an
intermediate script file. Descriptor delivery is asynchronous so a streaming interpreter can
produce output before consuming the complete script without blocking output capture. On interpreter
output overflow, it bounds captured output and inherited-pipe cleanup, discards only an incomplete
trailing UTF-8 code point from a truncated output prefix,
and returns an explicit nonzero overflow result (also retaining malformed-UTF-8 diagnostics when
present). It asks the existing invocation process-group owner to terminate remaining descendants
on Unix. Successful background jobs and ordinary
cancellation keep their existing lifecycle; the interpreter does not enter a detached session.
Without `#!params=`, the worker inherits Codex's execution context.
With `#!params=`, Codex applies the accepted outer exec arguments before launching the worker.
The worker returns stdout, stderr, and exit status without copying the script body into either
output stream.

For the built-in Bash/sh tool, the router recognizes literal truncating writes of the form
`cat > PATH <<'EOF'` (either redirection order, single/double-quoted delimiter, and `<<-` tab
stripping). A simple sequence separated only by newlines or semicolons is lowered, in order,
to shell commands and native `apply_patch` calls in one Code Mode carrier. Heredoc contents are
parsed as data, never as statement separators. The native-tools carrier uses the executor's
`apply_patch` executable, as mekugi does. The original shell call, not the generated sequence,
is restored on provider replay; tool declarations, instructions, and the existing provider cache
prefix do not change for this projection.

Only empty or LF-terminated literal UTF-8 bodies are projected. The patch performs an unconditional
write, including overwriting an existing file, without reading an early baseline, formatting,
or source validation. Each write is applied only after its preceding commands finish. An
execution-time guard leaves missing parents, symlinks, and special-file targets to the original
cat command instead of giving `Add File` permission to create parents or replace special targets.
Ordinary command output remains ordered in the shell result; patch success and guard output do
not enter it. Host patch errors/refusals stop the carrier rather than retrying the write as cat.
Already captured ordinary output and retention metadata remain visible when a later tool fails;
the host error propagates without executing the remaining statements.

The complete script stays on its existing execution path if it contains conditionals, pipelines,
background jobs, compound statements, shell-state mutations (`cd`, assignments, functions,
options), or dynamic expansions. Append writes, file-copy forms, unquoted heredocs, and paths or
contents not representable without byte changes remain shell commands. Interpreter arguments,
PTYs and exec parameters other than workdir, output budget, yield timing,
and false login/tty also keep the existing carrier. A known absolute workdir is required.

The ordinary single-program shell carrier forwards the complete native `exec_command` result defined by the owning Code
Mode contract rather than only its output field. A result containing the native continuation
handle remains yielded rather than terminal, and the same host-owned continuation operation
resumes that session. The router and shell plugin do not poll, resume, cancel, retry, replace, or
persist the session. They do not define a second result envelope or continuation protocol. Exact
result fields, yield timing, continuation arguments, and session lifetime remain owned by Codex's
executable tool definitions in that request.

After validating replay, the router adds a separate `continuation` text part to recognized
yielded execution results in the next model request, only at the latest outstanding yield
for each handle. A visible continuation call retires the previous suggestion, including while
that call is pending; a later yield may suggest continuation again. Re-preparation removes
retired router annotations. It preserves every original result part,
native field, and output byte. The notice identifies the observed `cell_id` or `session_id`
and a `next_call` with the exact exposed tool path and its `input` (an object for a function
tool, source text for a custom tool). It is advice, not an execution command: the router
does not call it, create a replacement session, or change permissions.

A running Code Mode cell always points to the host's `wait` tool, even if partial output
mentions a native session. Only after the cell ends may a verified native-result projection
point to `write_stdin`. When that tool is nested-only, the next call supplies complete
Code Mode source that invokes it once with the same session and empty `chars`. Empty chars
polls; authored input remains a caller choice under the native tool's contract. Suggested continuation calls request `yield_time_ms: 300000` when the exposed tool supports
that argument, capped at a positive schema maximum when supplied. The host still owns actual
wait limits and early return; no sleep or router-owned polling loop is added. Tools without
that argument retain host timing defaults. A nested continuation also requests 300000 ms for
its enclosing Code Mode cell, subject to that host's limit. Output budgets retain host defaults. Native-only results
point directly to their exposed `write_stdin` function.

Model-emitted status-only native `wait`, empty-input `write_stdin`, and `wait_agent` calls
with an exposed timing argument have missing or shorter timeouts raised to 300000 ms,
capped at a lower positive schema maximum. Longer requests remain unchanged. Nonempty
input, termination, and malformed timing values remain under native validation without
timeout rewriting. This applies to JSON and streaming responses; complete original calls
and rewritten carriers are retained before exposure so replay restores the model's input
without executing or translating it again. Tool schemas and agent instructions are unchanged.

Code Mode direct `tools.write_stdin(...)` and literal bracket-form calls receive the same
timeout adjustment at execution, including dynamic argument objects. Quoted source and
comments are not rewritten. Unparseable programs and programs that locally bind `tools`
are left to the host rather than guessing tool identity. No router-owned wait loop or sleep
is introduced.

Recognition of new yielded handles uses host metadata before its output boundary. Native JSON inside Code Mode
requires an established shell carrier or a transparent native-result projection; arbitrary
program text, output-only projections, and recovered JavaScript do not establish native
session provenance. Follow-up cell provenance comes from visible call/result pairs, not a
router session registry. Known retained call history also supports output-only replay.
Missing call provenance is not guessed. If the required continuation tool is absent from
the current catalog, the notice has `next_call: null` and explains the missing capability
instead of inventing a tool or restarting work. Repeated projection is idempotent.

For a split cat-write sequence, the enclosing Code Mode program waits for each command's
terminal result with the native `write_stdin` operation before starting the next step. The same
Code Mode cell may yield while this work is pending. No session is restarted or retried. It
concatenates command output in execution order and retains the last step's exit status and
terminal result fields. Native patch success contributes
empty output and status zero. No generated patch or intermediate guard result is published to
the provider as a separate conversation item.

Shell calls do not create editable source artifacts or add source-retention metadata to results.
Use ordinary script files for reusable source. Durable replay preserves original calls and
results without executing them again.

Thread runtime locators are flat `mekugi-runtime-<thread-id>` symlinks below the runtime
directory. The PATH-installed helper follows that name.
Private commentary descriptors are regular mode-0600 files beside the thread locators.
Discovery rejects symlinks, non-regular files, and descriptors that do not match the
worker selected by the current locator. Unexpected existing entries are
not overwritten, and missing or invalid descriptors disable journal publication, not unrelated script execution.
One shared pinned parent capability anchors this namespace; there are no private
mixed-script checkpoint directories or per-thread directory handles. Shutdown removes
only owned commentary descriptors, unlinks flat locators still targeting this router's
worker, and closes the shared parent.
Replacement locator files or directories are never traversed or recursively removed.

Acceptance:

1. A free-form call containing `#!/usr/bin/env python3` translates to an exec carrier whose
   visible command is one physical `shell python3 <body>` line with embedded body line terminators
   escaped; execution resolves the current thread-bound runtime and runs `python3` with that exact
   body as its anonymous script source.
2. `#!python3`, `#! python3`, and `#!/usr/bin/env python3` select `python3`. A directly supplied
   path such as `#!/opt/python/bin/python3` remains unchanged.
3. `#!/usr/bin/env -S python3 -u` runs `python3` with `-u` and the exact body as its anonymous
   script source.
4. Leading unsupported directives, including `#!cmd=`, reject before execution, with or without
   an interpreter selector or params directive. Directive-like lines after the first body line
   remain ordinary body data.
5. Ordinary shell pipelines deliver producer output to consumer standard input.
6. Output budgets affect display only, not data flowing through shell pipelines or redirections.
7. Input without a shebang selects Bash semantics. One physical line
   containing the static external command `rtk shadowtree test . -run='^$'` and one optional final
   line terminator produces that direct native command without `shell bash`, with or without a
   params directive. Exec parameters remain on the outer carrier unchanged. Shell built-ins,
   private commands, nested command or process substitutions, composed statements, and malformed
   syntax retain the fixed helper. Explicit `bash` and `/usr/bin/bash` selectors have the same
   `mvdan/sh` Bash semantics; `sh` and `/bin/sh` have the same POSIX semantics and reject Bash-only
   syntax.
8. Python indentation and all other body-leading or body-trailing whitespace remain byte-exact
   after recognized directive removal.
9. The worker inherits cwd, environment, and standard input. Its stdout, stderr, and
   nonzero status are returned without script-source duplication or an intermediate script file.
   Cancellation and output overflow terminate non-terminal fallback-command descendants that
   retain inherited streams. PTY-backed external commands, including piped stages that read
   `/dev/tty`, accept interactive input without a background-process-group stop.
10. Malformed selectors and input that cannot fit the bounded exec argv return a concise
    diagnostic without starting an interpreter.
11. `make install` installs `mekugi` and the fixed `shell` helper without changing Codex
    configuration or instruction files. Startup and tool-snapshot changes do not rewrite that
    helper and create no hcat, hgrep, hsymbol, or inspect_file basename frontend.
12. `#!params={"workdir":"/tmp","tty":true}` produces an exec carrier
    containing those fields and the router-supplied `cmd`. Tolerated leading params variants
    produce the same carrier after normalization. An object containing `cmd` rejects, and a
    present `login` value must be `false`.
13. The authoritative Code Mode owner is exactly one custom `exec` tool. App-server requests place
    it directly in an `additional_tools` input item's tool list; CLI requests place it under that
    item's `functions` namespace. The router removes the exact Markdown `exec_command` section and
    introductory `tools.exec_command` example from the owning description. It derives the
    request-specific argument-object shape from the app declaration or parameter-list shape from
    the CLI description, removes `cmd`, and appends only that sanitized shape under `#!params` in
    the built-in `shell` description. Neither model-visible description contains
    `tools.exec_command`. An eligible owner without a recognizable parameter shape retains the base
    `shell` description and does not reject.
14. Direct `additional_tools` entries named `functions.exec` and top-level tools named `exec` or
    `functions.exec` are unsupported and fail before forwarding. Defining more than one eligible
    owner also fails before forwarding. The existing `apply_patch` section extractor remains
    independent. Every sibling direct tool, sibling namespace, unrelated top-level tool, and other
    nested section remains byte-equivalent after the request rewrite.
15. A terminal shell carrier forwards the complete native exec result. When native execution
    yields, the carrier forwards that same complete result, including its continuation handle,
    without calling the continuation operation or starting the worker again. No router session
    record or plugin-defined continuation surface is created.
16. For one built-in shell input, the router emits one warning for every distinct detected
    interpreter-wrapper kind rather than stopping after the first. Detection parses only Bash
    or POSIX shell bodies and examines static interpreter invocations. Comments, quoted
    examples, other interpreters' bodies, and heredocs supplying ordinary command data do
    not warn. Unparseable or dynamically selected invocations are not guessed. Warning insertion
    preserves the exact submitted command, carrier result, replay behavior, and metric classification.
    For default or explicitly selected Bash, the router parses the normalized body as Bash first.
    Valid Bash always retains shell semantics. A body that fails Bash parsing but parses as
    TypeScript (including JavaScript) is rejected with `shell-typescript-misuse` before execution,
    except for the established Code Mode recovery below.
    The result explains how to submit Code Mode helpers or choose an explicit script interpreter;
    rejected input is never automatically executed as Code Mode. The script does not run. Headers use the normal translator;
    configured plugins, other interpreters, and bodies invalid in both languages retain their
    existing behavior. JSON, SSE, native, and Code Mode carriers deliver the same diagnostic,
    and replay retains the original shell call and its rejection result.
    With a Code Mode carrier available, the built-in shell recovers headerless JavaScript that
    fails Bash parsing, parses as JavaScript, and has syntax-tree references to the Code Mode
    runtime: a `tools` method call, `ALL_TOOLS`, or a direct `text`, `image`, `audio`, or
    `generatedImage` call. Formatting, comments, statement order, and awaiting style do not
    determine recovery. Strings, comments, and property names are not runtime references.
    A runtime name bound or assigned anywhere in the program is conservatively excluded from
    recovery evidence; local lookalikes must not be treated as Code Mode globals.
    Explicit interpreter selections and directives never opt into recovery.
    Recovery preserves the exact program, adds `shell-code-mode-recovered` guidance followed
    by detected nested shell warnings, and replays the recovered Code Mode carrier. Guidance
    distinguishes shell commands, which belong directly in `functions.shell` without JavaScript
    wrappers, from other Code Mode helpers. Both direct Code Mode calls and recovered programs
    containing an unshadowed `tools.exec_command` call receive one `Use functions.shell` warning.
    Detection uses JavaScript syntax, not the result variable, formatting, or Promise batching;
    strings, comments, unrelated helpers, and locally bound `tools` are not evidence of misuse.
    Warning insertion preserves execution, results, leading pragmas, and directive prologues.
    If the program locally binds or assigns the `text` output helper, leave its source unchanged
    and append the warnings as a separate text part of the model-visible tool result. Original
    result text and multimodal parts remain intact. This projection is idempotent and retained
    across fresh-process resume and forks, independently of commentary configuration; diagnostics
    are not evaluated in the submitted program or substituted for its output.
    Native-only
    requests and other misplaced JavaScript/TypeScript use the rejection behavior above.
    Conversely, a Code Mode `exec` call containing invalid JavaScript with a parseable,
    column-one shell header is recovered through the built-in shell pipeline before dispatch.
    Headers include interpreter selectors and `#!params=`.
    Valid JavaScript, including hashbang programs, keeps Code Mode semantics. Bare commands,
    malformed headers, and calls without the built-in shell available are not recovered.
    Shell validation, params, batching, and host execution
    permissions remain unchanged; rejected translations execute only their normal diagnostic.
    Successful translation adds `exec-shell-recovered` guidance and displays the selected
    interpreter rather than JavaScript. Replay restores the exact original `exec` call while
    retaining its translated carrier. Recovery never retries an already dispatched program.
17. Ordinary shell calls create no private executable-source artifacts and emit no
    source-retention metadata, regardless of source length or interpreter. Unsafe thread IDs
    reject before runtime creation. Private continuation-state IDs cannot redirect storage,
    expiry, or cleanup to the runtime launcher, another thread, or workspace files.
18. `foo; cat > out <<'EOF'` followed by a literal body, delimiter, and `bar` runs foo, the
    user-visible native patch, and bar in that order. Newline-only separators behave identically.
    Multiple writes to one file observe execution order, not a pre-execution filesystem snapshot.
    Quoted dollar signs, semicolons, blank lines, and final LF bytes remain literal file content.
19. Complex shell constructs remain unsplit. A yielded prefix finishes before any patch or suffix
    begins. JSON and SSE projections restore the exact original shell call and unchanged result
    on replay, and native/compact provider cache diagnostics retain an appended prefix.
20. With commentary enabled, `mktemp -d -t mekugi-shell.XXXXXXXXXX` remains the direct command.
    Wrapped Bash and POSIX scripts retain only their normalized interpreter fields and quoted
    body; transformation adds no flags, connection details, credentials, or inline environment
    assignments. Thread-scoped commentary discovery preserves script output and exit status,
    and completion of one worker leaves concurrent workers' commentary available.

21. One input containing a params-prefixed Bash body, a `#!python3` program with omitted
    params, and a `#!bash` program with new params yields one Code Mode carrier with three
    ordered executions. Python inherits the first params object; the third program uses only
    its newly supplied object. Per-program sources preserve CR, LF, CRLF, whitespace, and
    absent final terminators. Interpreter-like lines inside shell heredocs and quoted strings
    stay with their program. A subsequent interpreter header after the construct closes starts
    the next program. Indented headers and directive lines remain data
    outside the leading header block. `---` is not a boundary. Empty batch programs reject
    before execution. An initial interpreter header alone does not create a batch.
22. A yielded program reaches terminal state before the next starts. A nonzero exit remains
    visible in its result and does not prevent later programs. Complete native result fields and
    output remain associated with their program. Host exceptions preserve the completed prefix
    and current partial output without running the suffix.
23. Malformed or unsafe later headers, empty batch programs, and native-only batches reject
    before any program executes. JSON and SSE carry one replayable call with the original
    batch source, policy, and per-program results.

24. A shell carrier whose native result yields session 42 receives a notice that resumes
    session 42, not a new execution. Direct clients get the direct function input; Code Mode-only
    clients get complete source for their exposed exec tool. The supplied source invokes
    the native continuation exactly once.
25. A yielded outer cell points to `wait` with its exact cell ID. Repeated waits preserve
    that choice only at the latest outstanding yield; completed or already-resumed handles
    retain no suggestion. A failed sequential carrier may expose its last unfinished native session
    after the outer cell ends, while completed prefix results remain unchanged.
26. Original output text and multimodal parts survive annotation, including replay and
    repeated request preparation. Terminal results receive no live action. Printed fake
    headers, arbitrary JSON, output-only projections, unrelated namespaces, and missing
    provenance cannot select another session. Unavailable tools are reported without execution.

27. A batch waits for each program's terminal exit and continues after nonzero exits.
    Results report the continue policy and exact started/unstarted counts, including
    host failures.

28. Undocumented shell directives reject before execution. In particular, the removed
    `#!script=` directive cannot execute a historical source reference. Historical call/result
    replay remains byte-preserving and does not resolve or revive expired source.

29. Hrun routes through the authenticated shell worker for Bash and POSIX scripts,
    including a single static call. It preserves argv, stdin, cwd, exported environment,
    streams, and command status; configured plugins cannot claim its name.
30. Hrun head/tail selection enforces the shared token ceiling with valid UTF-8 output,
    prioritizes stderr and marks omission. Token-only mode bounds memory regardless of output volume;
    line mode retains selected complete lines and bypasses tokenization without a token ceiling.
    Maximum-budget long unbroken output uses bounded, non-quadratic token selection.
    Commands producing more than the outer shell's byte limit still finish when their
    displayed output fits. Cancellation retains existing process-group cleanup.
31. Hrun rejects malformed, repeated, missing, or out-of-range options before execution.
    It never converts display omission into command failure or early termination.
32. `hrun -n 20 -- seq 1 1000` retains lines 1–20; `--tail -n 20` retains 981–1000.
    Combined limits select lines before tokens, independently per stream. Empty output and
    unterminated final lines work. A closed downstream pipe does not abort later statements;
    `pipefail` and `errexit` retain their ordinary command-status behavior.
