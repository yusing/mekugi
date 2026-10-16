# Terminal primitives

## Purpose and scope

Provide terminal input, scrolling, raw-mode lifetime and Mermaid drawing without
feature state or backend control. Consumers own pane composition, key bindings,
focus, selection and the enclosing session lifetime.

## Behavior

Raw-mode cleanup restores the caller's terminal state. Canceling an input reader
preserves caller descriptors and their shared file-status flags. Mouse reports
decode into input facts; they do not select a pane or execute a command.

Faint-support detection reports terminal capability. Consumers choose fallback
colors and meaning. Mermaid drawing accepts a bounded supported flowchart subset,
respects available width and returns unavailable for unsupported syntax rather
than emitting a partial diagram. The text consumer owns fallback presentation.

## Acceptance

PTY tests cover cancellation, descriptor lifetime and terminal restoration.
Input tests cover mouse decoding and scrolling. Mermaid fixtures cover supported
directions, labels, crossings, bounds and unsupported input. Native-session and
offline-replay acceptance exercise these primitives through the actual renderer.
