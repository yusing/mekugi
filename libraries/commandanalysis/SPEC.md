# Command analysis

## Purpose and scope

Recognize deterministic command, executable, and literal source facts without
running code or reading the target filesystem. Approval, evidence capture, and
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

## Ownership and acceptance

Shell syntax and wrapper operands live in `shellsyntax`. Shared JavaScript AST
path and call recognition lives in `sourceanalysis/sourceparse`; it rejects
optional/computed member access and omits argument comments.

Owner tests cover literal/dynamic classification, polling, bounds, directory and
shell resolution, and unchanged command record encoding. Application tests cover
capture scope, retained history, native trace matching, previews, and display.
No recognized source fact establishes host completion or authorizes execution.
