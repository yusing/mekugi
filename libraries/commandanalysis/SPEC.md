# Command analysis

## Purpose and scope

Recognize command, executable, source, and bounded execution-scope facts without
executing the observed program. Approval, evidence capture, and
presentation remain with the consumer.

## Command inputs

Stock command arguments retain the command text, working directory, and shell.
Relative directories resolve only against an explicitly selected absolute
workspace; an absolute command directory is usable independently. An omitted
shell uses the supplied session shell. Commands for a nonlocal environment are
excluded from local observation.

Literal JavaScript extraction visits source in lexical order without simulating
execution. It returns known literal commands alongside a separate indication of
dynamic command access. A consumer must retain that distinction when choosing
observation coverage. Pure polling with no input does not drive a command;
ambiguous or nonempty process input remains dynamic. Extraction preserves its
existing source-size and command-count bounds.

The JSON argument reader remains a dependency of the inactive instruction loader;
normal literal extraction resolves decoded values directly. JSON argument decoding
retains its existing permissive field semantics. Literal
source recognition accepts only fully decoded literal objects and excludes any
explicit environment selector, including null. These input forms deliberately
have different recognition coverage.

## Version-control effects

Shared VCS facts distinguish possible worktree changes from remote writes. Global
option values are not subcommands. Known readers remain read-only unless their
options explicitly write output; unknown commands retain conservative effect
classification. GitHub CLI request method, fields and query source determine its
remote-write classification. These facts perform no alias lookup or process I/O
and grant no approval. Remote-write policy supplies any local Git alias lookup.

## Execution scope

`execscope` derives bounded write-scope candidates from shell/interpreter syntax,
shared literal bindings, and supported producer queries. It never expands source
by executing it or consults a router working directory. Consumers supply the
selected directory, environment facts and retained-change resolver. Dynamic or
unknown targets remain separate from recognized literal paths.

Named formatter targets, including `bun fmt` operands, accept bounded lexical
brace expansion in Bash, including literal alternatives and ranges. Parameters,
command substitutions and other dynamic expressions remain unresolved;
expansion never executes shell code.

Source reads use bounded nonblocking opens. Read-only producer queries run only
validated argument vectors under the shared deadline; they never run shell query
text. VCS and fixer providers share the classifier registry. Capture, authorization,
writer-window lifetime and retained evidence remain application/feature owners.
Bindings share path derivation with literal preview evaluation without making
predicted content an observed execution result.

Providers never execute interpreter source, a writer stage or a remote request,
or invoke configured hooks, preprocessors, filters or monitors. Missing tools and
query timeouts reduce coverage without replacing the stock result. Filesystem-
dependent destinations, such as copying into an existing directory, resolve when
the consumer captures scope, including every candidate an earlier statement could
select. Recognized literal paths never erase separate dynamic-source coverage.

## Ownership and acceptance

Shell syntax, wrapper operands, incomplete-stream parsing and literal directory
steps live in `shellsyntax`. Shared JavaScript AST
path and call recognition lives in `sourceanalysis/sourceparse`; it rejects
optional/computed member access and omits argument comments.

Owner tests cover literal/dynamic classification, polling, bounds, directory and
shell resolution, and unchanged command record encoding. Application tests cover
capture scope, retained history, native trace matching, previews, and display.
No recognized source fact establishes host completion or authorizes execution.
