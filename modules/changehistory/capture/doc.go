// Package capture observes filesystem effects without executing host input.
// Capture combines source-named baselines with optional private workspace
// checkpoints; PreviewOnly keeps provisional capture scope-only.
// Reconcile and ReviewPatch compare captured before-state with current endpoints.
// Callers retain host completion, window coordination, and evidence publication.
package capture
