package executionobservation

import (
	"context"

	sessionreview "github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"

	"github.com/yusing/mekugi/modules/changehistory/presentation"
	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
)

// Exercise the same snapshot path used by the event-stream consumer.
func (s *mekugiReplayStore) liveDiffSnapshotFiles(ctx context.Context, scope previewmodel.Scope) ([]presentation.File, error) {
	data, err := s.liveDiffSnapshot(ctx, scope)
	if err != nil {
		return nil, err
	}
	return data.Publication.Files, nil
}

func (s *mekugiReplayStore) liveDiffSnapshot(ctx context.Context, scope previewmodel.Scope) (*presentation.Snapshot, error) {
	projection := sessionreview.Projection{Changes: s.changes, Retained: s.changeScope()}
	if _, err := projection.Apply(ctx, sessionreview.Event{Kind: "scope", Scope: &scope}); err != nil {
		return nil, err
	}
	return projection.Data, nil
}
