package router

import (
	"context"
	"crypto/rand"
	"sync"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
)

var testLiveReviews sync.Map

func newLivePreview(ctx context.Context) *previewmodel.Session {
	review := newSavedReview(ctx, new(previewmodel.Service))
	session := review.Preview()
	testLiveReviews.Store(session, review)
	return session
}

func testReview(service *previewmodel.Session) *savedReview {
	value, ok := testLiveReviews.Load(service)
	if !ok {
		panic("preview fixture is missing its application review owner")
	}
	return value.(*savedReview)
}

func currentLivePreview(service *previewmodel.Session, id string) (previewmodel.Preview, bool) {
	for _, value := range service.Current() {
		if value.ID == id {
			return value, true
		}
	}
	return previewmodel.Preview{}, false
}

func boundPreview(value previewmodel.Preview) previewmodel.Preview {
	if value.ID == "" {
		value.ID = "bounded"
	}
	if value.Thread == "" {
		value.Thread = "thread"
	}
	service := newLivePreview(context.Background())
	setLiveScope(service, previewmodel.Scope{Workspaces: map[string]map[string]bool{value.Workspace: {value.Thread: true}}})
	sub := testReview(service).Subscribe()
	service.Update(value, false)
	for _, event := range testReview(service).TakePreviews(sub) {
		if event.Preview != nil {
			return *event.Preview
		}
	}
	return previewmodel.Preview{}
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
func resyncLiveScope(service *previewmodel.Session, scope previewmodel.Scope) {
	testReview(service).ResetScope()
	setLiveScope(service, scope)
}

var testLiveTurns sync.Map

func publishLiveTurn(service *previewmodel.Session, active bool) {
	if active {
		for workspace, threads := range testReview(service).Scope().Workspaces {
			for thread := range threads {
				turn := [3]string{workspace, thread, rand.Text()}
				testLiveTurns.Store(service, turn)
				service.Enable()
				service.BeginTurn(workspace, thread, turn[2])
				return
			}
		}
		panic("turn fixture requires admitted scope")
	}
	if value, ok := testLiveTurns.Load(service); ok {
		turn := value.([3]string)
		service.FinishTurn(turn[0], turn[1], turn[2])
	}
}
