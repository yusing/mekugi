# Verified immutable-baseline targets

## REQ-SELECT-001 — Verified immutable-baseline targets

Every explicit target resolves against the script's invocation-bound file's immutable invocation
baseline. A row first compares its four-digit hash with the exact content at its one-based
logical-line hint. If they differ or the hint is out of bounds, mekugi scans the same immutable
baseline and resolves the row only when exactly one line has that hash. No match is
`row-missing` when the hint is out of bounds and `row-stale` otherwise. Multiple matches are
`row-stale`; mekugi never chooses among duplicate content. The 16-bit hash retains an accepted
approximately 1-in-65,536 random false-acceptance residual for a candidate line.

If baseline resolution fails after earlier commands have pending edits, the evaluator may treat
the line number as a pending-content coordinate. This succeeds only when the pending line has the
supplied hash and its complete span maps back to exactly one unchanged immutable-baseline line.
Insertions at that baseline line's boundaries may shift the coordinate; an insertion or
replacement inside the line makes it ineligible. Content introduced or modified by a pending edit
does not become targetable. The resolved target remains the mapped baseline line, so conflict and
transaction semantics do not acquire a second editing baseline.

Both endpoints of a range must verify independently and remain ordered. This verifies the
endpoint lines, not every byte in the inclusive region: changed interior content can still
be selected when both endpoints match. Row hashes are target checks, not file versions or
concurrent-writer protection; caller coordination is required by `REQ-OUTPUT-001`.
An anchored text target
searches the verified baseline suffix. If its row is missing or stale, the row is redundant only
when the complete immutable baseline contains exactly the requested number of non-overlapping
literal matches; that exact set is selected. Extra or missing global matches preserve the row
failure. An unanchored text target searches the complete baseline, exactly as defined by
`REQ-SCRIPT-001`.
Pending edits never alter the baseline identity, literal search, matches, or target positions.
They may supply only the verified coordinate fallback above. Content introduced or modified by
any command is not targetable in that script. Dependent edits require successful application and
a later invocation. Exact authored current text may be used as an unanchored literal target
without another read; other introduced content requires fresh verified references.

Independently detectable row-missing, row-stale, occurrence-missing, and target-order failures
are collected across later commands whose file baselines can still be evaluated safely. The
transaction remains atomic. Dependency-sensitive file, conflict, and language failures
still stop at their authoritative boundary.

Resolution produces one nonempty baseline span for a line or range and one or more
nonempty spans for a text target. A mutation over multiple spans validates and registers
all of them or none.

Acceptance:

1. A copied hash-bearing reader row verifies complete content, including indentation, at its line hint or at
   one uniquely matching relocated row.
2. Missing and changed rows reject without choosing an unverified substitute. Duplicate baseline
   rows reject unless the supplied pending coordinate maps to one unchanged baseline row.
3. Inclusive ranges resolve both endpoints independently and reject reversed resolved order.
4. Text targets select the requested first N non-overlapping matches, including matches that
   span logical lines or end in LF, from the verified
   anchor or byte zero through EOF and reject incomplete multiplicity. A missing or stale anchor
   is ignored only when the literal's complete-baseline multiplicity equals N exactly.
5. Independent targets retain their original meaning after pending edits; introduced or modified
   content cannot be addressed within the same invocation. A later invocation may target exact
   known current text without a row. A post-edit coordinate may identify one
   unchanged baseline row shifted by earlier boundary insertions.
