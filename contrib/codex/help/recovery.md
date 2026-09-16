# Rejected-script recovery

Use `functions.hpatch_recover` to repair the latest retained rejected script, preserving
unrelated prepared edits. Choose one payload form:

- For a wholly row-stale rejection, supply every listed current `HANDLE` handle followed by
  its corrected ordinary HPATCH/2 target.
- For a parsed command's target or value, use `HANDLE target TARGET` or `HANDLE value VALUE`.
  Values accept quoted strings and normal heredoc/text framing. Use each handle once per
  payload; unrelated fields and commands stay unchanged. This avoids editing script delimiters.
- For framing, paths, conflicting commands, or other script corrections, use ordinary
  target-bearing `type`/`add` mutations against retained-script text.
  Generated-source line numbers in diagnostics are not editable recovery targets.

Target-only example:

```text
maple "return oldResult, nil"
```

Copy every current handle exactly and supply a different target for each in one payload.

Script-text mutations edit the retained rejected script, not workspace files. Use the diagnostic's
verified script rows or exact known literals; omit `in`, `new`, `mv`, and `rm`.
For example, `type "bad value" "fixed value"` changes that exact retained text.
Use ordinary value framing and keep the two payload forms separate.

Both forms preserve untargeted text and reevaluate the complete script atomically.
A re-rejection becomes the next baseline: use its script rows and refreshed command handles.
Invalid corrections leave the workspace and retained baseline unchanged.
