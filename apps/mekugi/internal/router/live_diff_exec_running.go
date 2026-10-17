package router

import (
	"context"
	"slices"
	"time"

	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
)

// The window registry owns display cancellation, not process continuation.
// Source-card polling never sweeps or re-runs a provider query. Completed
// boundaries can compare private snapshots; neither path persists evidence.
var execRunningPreviewSlots = make(chan struct{}, 16)

func (r *execWindowRegistry) preview(ref string, observation changecapture.Observation, broker *previewmodel.Session, workspace, thread, caller string, snapshots *changecapture.Workspaces) {
	if r == nil || broker == nil {
		return
	}
	observed := observation
	observation = previewmodel.SelectEndpoints(broker.Context(), observation)
	if len(observation.Files) == 0 && r.tracker == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	window := r.find(ref)
	if window == nil || window.previewCancel != nil || !window.closed.IsZero() || window.background {
		return
	}
	select {
	case execRunningPreviewSlots <- struct{}{}:
	default:
		return
	}
	ctx, cancel := context.WithCancel(broker.Context())
	window.previewCancel = cancel
	tracking := window.tracking
	if tracking == nil {
		tracking = newExecPreviewTrack(r.tracker, window.thread, window.turn, observed.Commands)
		window.tracking = tracking
	}
	if tracking != nil {
		tracking.streamable = make(map[string]bool)
		for _, source := range previewmodel.StreamableSources(ctx, observation) {
			tracking.streamable[tracking.Source(source, "")] = true
		}
	}
	turn := window.turn
	go func() {
		defer func() { <-execRunningPreviewSlots }()
		capture := &execPreviewCapture{registry: r, ref: ref, observation: observed, snapshots: snapshots}
		runExecScopePreview(ctx, broker, observation, previewmodel.Preview{ID: "running:" + ref, Workspace: workspace, Thread: thread, Turn: turn, Caller: caller}, tracking, capture)
	}()
}

// The running watcher owns both source cards and completed Activity evidence.
// Only cards require prediction. Receipts compare the original capture scope.
type execPreviewCapture struct {
	registry    *execWindowRegistry
	ref         string
	observation changecapture.Observation
	snapshots   *changecapture.Workspaces
	completed   int
	text        string
	calls       []string
	next        time.Time
}

func (c *execPreviewCapture) update(ctx context.Context, broker *previewmodel.Session, preview previewmodel.Preview, tracking *execPreviewTrack, state execPreviewState) {
	if state.completed <= c.completed || time.Now().Before(c.next) {
		return
	}
	c.next = time.Now().Add(500 * time.Millisecond)
	view := c.registry.view([]string{c.ref}, false)
	comparison := changecapture.Comparison{Excluded: view.excluded, Workspace: preview.Workspace, Claims: view.claims}
	// Only unnamed effects require an isolated workspace snapshot. A sibling
	// on another target must not suppress captured named baselines.
	if view.snapshot && len(view.overlaps) == 0 && len(view.background) == 0 {
		comparison.Workspaces = c.snapshots
	}
	scope := c.observation
	for _, path := range view.created {
		scope.AddCreatedPath(path.Data, path.Tree)
	}
	// This receipt is an observed endpoint, not a claim of complete intermediate
	// write history. Returning to the baseline must withdraw the earlier endpoint.
	scope.RepeatedPaths = false
	result := changecapture.Reconcile(ctx, scope, comparison)
	if result.SnapshotError != nil {
		return
	} // Keep the boundary pending for retry.
	files, complete := result.Files, result.Complete
	// Another writer may have started while the filesystem comparison ran.
	if current := tracking.state(); current.completed != state.completed || !slices.Equal(current.calls, state.calls) || ctx.Err() != nil {
		return
	}
	text := editReceiptText(preview.Workspace, mekugiHistory{Source: previewmodel.CaptureTool(c.observation), ReviewFiles: files, ExecOutcome: &changecapture.Outcome{Labels: c.observation.Labels}})
	if (complete || text != "") && (text != c.text || text != "" && !slices.Equal(state.calls, c.calls)) {
		preview.ID, preview.Files = "observed:"+c.ref, files
		preview.EditReceipt, preview.HostItems, preview.Complete = text, slices.Clone(state.calls), true
		preview.Status, preview.Footer = "", "observed edit"
		broker.Update(preview, false)
		c.text, c.calls = text, slices.Clone(state.calls)
	}
	if complete {
		c.completed = state.completed
	}
}

func runExecScopePreview(ctx context.Context, broker *previewmodel.Session, observation changecapture.Observation, frame previewmodel.Preview, tracking *execPreviewTrack, capture *execPreviewCapture) {
	endpoint := broker.ObserveEndpoints(ctx, observation, frame)
	defer endpoint.Close()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		state := tracking.state()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-state.changed:
		}
		state = tracking.state()
		if capture != nil && tracking != nil {
			capture.update(ctx, broker, endpoint.View(), tracking, state)
		}
		if endpoint.Advance(ctx, state.tracked, state.settled) && (capture == nil || tracking == nil) {
			return
		}
	}
}
