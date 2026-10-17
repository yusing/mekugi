package capture

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync/atomic"

	"github.com/yusing/mekugi/contracts/activitydetail"
	"github.com/yusing/mekugi/contracts/scope"

	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/changehistory/internal/retained"
	"github.com/yusing/mekugi/modules/changehistory/presentation"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

// Service combines retained change operations with invocation-owned checkpoints.
// An empty directory selects retained operations only, as used by command workers
// and offline readers. Construction acquires no checkpoint resources.
type Service struct {
	changes.Service
	directory      string
	workspaces     *Workspaces
	review         scope.Key[reviewPublication]
	reviewRevision atomic.Uint64
}

type reviewPublication struct {
	snapshot *presentation.Snapshot
	view     presentation.ReviewView
}

func (s *Service) PublishReview(store *scope.Store, snapshot *presentation.Snapshot, workspace, coverage, failure string) {
	if s == nil || store == nil {
		return
	}
	review := s.review.Value(store)
	if review.snapshot == snapshot && review.view.Workspace == workspace && review.view.Coverage == coverage && review.view.Failure == failure {
		return
	}
	review.snapshot = snapshot
	review.view = presentation.ReviewView{Workspace: workspace, Coverage: coverage, Failure: failure, Revision: s.reviewRevision.Add(1)}
	if snapshot != nil {
		review.view.Publication = snapshot.Publication
	}
}

func (s *Service) ReviewView(store *scope.Store) presentation.ReviewView {
	if s == nil || store == nil {
		return presentation.ReviewView{}
	}
	return s.review.Value(store).view
}

func (s *Service) ActivityDetail(store *scope.Store, request activitydetail.Request) activitydetail.Result {
	if s == nil || store == nil {
		return activitydetail.Result{}
	}
	return s.review.Value(store).snapshot.ActivityDetail(request)
}

func New(service *history.Service, directory string) (*Service, error) {
	if directory != "" {
		if !filepath.IsAbs(directory) {
			return nil, errors.New("capture storage requires an absolute retained directory")
		}
		directory = filepath.Clean(directory)
	}
	retained, err := retained.New(service)
	if err != nil {
		return nil, err
	}
	return &Service{Service: retained, directory: directory}, nil
}

func (s *Service) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.directory != "" {
		s.workspaces = newWorkspaces(ctx, s.directory)
	}
	return nil
}

func (s *Service) Stop(context.Context) error {
	s.workspaces.Close()
	s.workspaces = nil
	return nil
}

// Checkpoints borrows the invocation owner for its selected retained directory.
// A retained-only service supplies no capture resources. The caller must finish
// its observation windows before composition stops the service.
func (s *Service) Checkpoints(directory string) (*Workspaces, error) {
	if s == nil || s.directory == "" {
		return nil, nil
	}
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != s.directory {
		return nil, fmt.Errorf("capture storage does not match retained directory %q", directory)
	}
	if s.workspaces == nil {
		return nil, errors.New("capture resources have not started")
	}
	return s.workspaces, nil
}
