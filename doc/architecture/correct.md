# Router-only rejected-script recovery

## CTR-CORRECT-001 — Router-only rejected-script recovery

The router owns rejected-script ancestry, command handles, request-visible baseline
selection, correction grammar, limits, diagnostics, and complete-script reevaluation.
It rebuilds corrected text through the generic bounded text editor and sends the result
through the ordinary edit or mixed-preflight dispatch. The engine and public library
APIs have no recovery history or recovery mode.

Recovery selects only durable calls visible to the current request plus calls evaluated
in that response. Resume, forks, and shared routing identities do not broaden this view.
Malformed or conflicting corrections change neither the workspace nor the retained
baseline. Comparable target identity prevents a target-only correction from resubmitting
the same target under different spelling.

Mixed-script continuation belongs to `CTR-PLUGIN-001`, not rejected-script recovery.
A preflight failure before any executable carrier exists may use router-owned
script-text recovery because it cannot have execution effects. After carrier retention,
recovery may only direct the call to its continuation; translated or retained state is
never reported as executed application.
