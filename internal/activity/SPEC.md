# Parsed activity operations

## Purpose and scope

Interpret classified operation text as renderer-neutral content for activity feeds
and output dialogs. Host adapters supply the text and retain authority over tool
identity, execution, outcomes and captured changes. Parsing neither evaluates
source nor establishes that an operation ran.

## Behavior

Blank lines separate operations except inside fenced source. Each operation keeps
its verb, label, source language/body, explicit timeout annotations and read
targets. Unknown labels remain available rather than being discarded. Read ranges
belong to file reads; target-only operations retain their literal operands. Skill
program invocations retain program source rather than becoming skill reads.

Edit receipts retain their source across adjacent tool-managed-file summaries.
An edit with no resolved target explicitly says that paths are unavailable.
Generated edit operations omit their diff bodies from the feed; authored Markdown
and retained change evidence are unaffected.

The same content values can carry authored messages and reasoning supplied by host
adapters. Their kind does not establish host execution state.

The parser owns no feed, output retention, grouping across invocations or terminal
state. Consumers attach observed timing/outcomes, aggregate read invocations and
choose rendering. Source values contain no styles, geometry, callbacks or live
handles. Shared fence and inline-code recognition belongs to `sourceanalysis`.

## Acceptance

Parser cases cover reads, literal operands, timeouts, fenced programs, edit-source
inheritance and unavailable targets. Live/restored feed and output-dialog checks
exercise the same parser, preserving invocation membership, actual rendered rows
and source-copy behavior.
