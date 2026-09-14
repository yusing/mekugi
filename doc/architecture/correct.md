# Router-only rejected-script recovery

## CTR-CORRECT-001 — Router-only rejected-script recovery

The router owns rejected-script ancestry, command handles, request-visible baseline
selection, correction grammar, limits, diagnostics, and complete-script reevaluation.
It rebuilds corrected text through the generic bounded text editor and sends the result
through the ordinary edit dispatch. The engine and public library APIs have no recovery
history or recovery mode.

Recovery selects only durable calls visible to the current request plus calls evaluated
in that response. Resume, forks, and shared routing identities do not broaden this view.
Malformed or conflicting corrections change neither the workspace nor the retained
baseline. Comparable target identity prevents a target-only correction from resubmitting
the same target under different spelling.

Mixed-script continuation belongs to `CTR-PLUGIN-001`, not edit-only recovery. Recovery
may direct a mixed call to an already-retained continuation, but translated or retained
state is never reported as executed application.
