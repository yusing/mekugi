# Safe display text

## Purpose and scope

Normalize untrusted text for retained content and terminal presentation without
owning layout, themes, feature state or I/O.

## Behavior

Remove control sequences, including cursor movement, clipboard writes and terminal
titles. Preserve readable Unicode, combining marks, joiners and newlines; expand
tabs to four spaces. Callers may explicitly retain valid SGR color sequences.
Plain-content consumers strip colors as well. Authored Markdown source preserves
tabs while removing controls, so code copying retains the submitted bytes.

Structured-output parsers may strip escape sequences before interpreting fields,
preserving tabs and line delimiters. Displayed fields still use full sanitization.

Returned strings remain independent of reusable scratch storage. Sanitization does
not change the original tool input, captured evidence or model-visible output.

## Acceptance

Tests cover control removal, optional colors, Unicode and concurrent scratch reuse.
Diff, Markdown, activity and native-session renderer tests cover their composed
output and source-copy behavior.
