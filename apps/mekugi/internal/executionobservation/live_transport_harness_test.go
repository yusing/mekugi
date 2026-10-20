package executionobservation

import (
	"context"
	"sync"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"

	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
)

var testLiveReviews sync.Map

func newLivePreview(ctx context.Context) *previewmodel.Session {
	review := sessionreview.New(ctx, previewmodel.New(changecapture.ReadFile), nil)
	session := review.Preview()
	testLiveReviews.Store(session, review)
	return session
}

func testReview(service *previewmodel.Session) *sessionreview.Review {
	value, ok := testLiveReviews.Load(service)
	if !ok {
		panic("preview fixture is missing its application review owner")
	}
	return value.(*sessionreview.Review)
}

func currentLivePreview(service *previewmodel.Session, id string) (previewmodel.Preview, bool) {
	for _, value := range service.Current() {
		if value.ID == id {
			return value, true
		}
	}
	return previewmodel.Preview{}, false
}

// Fixtures enter through the same neutral scope and root-turn APIs as attachment.
func setLiveScope(service *previewmodel.Session, scope previewmodel.Scope) {
	service.Enable()
	for workspace, threads := range scope.Workspaces {
		for thread, admitted := range threads {
			if admitted {
				testReview(service).IncludeThread(workspace, thread, true)
			}
		}
	}
}
