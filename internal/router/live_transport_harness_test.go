package router

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
)

const maxLiveDiffEventBytes = 1 << 20

const liveDiffEventsPath = "/internal/live-diff"

// Test-only bridges let existing subprocess rendering tests drive local state.
// The bootstrap file is a private connection capability, not changing view data.
type liveDiffConnection struct {
	Endpoint string
	Token    string
}

var testLiveConnections sync.Map
var testLiveCoverage sync.Map
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

func testLiveConnection(owner any) liveDiffConnection {
	value, _ := testLiveConnections.LoadOrStore(owner, liveDiffConnection{Token: rand.Text()})
	return value.(liveDiffConnection)
}
func setLiveEndpoint(b *previewmodel.Session, endpoint string) {
	c := testLiveConnection(b)
	c.Endpoint = endpoint
	testLiveConnections.Store(b, c)
}
func liveDescriptor(b *previewmodel.Session) liveDiffConnection { return testLiveConnection(b) }
func serveLiveEvents(b *previewmodel.Session, w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+liveDescriptor(b).Token {
		http.Error(w, "invalid live diff capability", http.StatusUnauthorized)
		return
	}
	coverage := make(chan reviewEvent, 1)
	testLiveCoverage.Store(b, coverage)
	defer testLiveCoverage.Delete(b)
	sub := testReview(b).Subscribe()
	defer testReview(b).Unsubscribe(sub)
	controller := http.NewResponseController(w)
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	write := func(event reviewEvent) error {
		data, err := json.Marshal(event)
		if err != nil || len(data) > maxLiveDiffEventBytes {
			return errors.New("live diff event exceeds capacity")
		}
		if err := controller.SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
			return err
		}
		if _, err := w.Write(append(data, '\n')); err != nil {
			return err
		}
		return controller.Flush()
	}
	// The snapshot barrier must precede the separate preview mailbox.
	if write(<-sub.Events()) != nil {
		return
	}
	// Restore retained turn state before replaceable preview snapshots.
	select {
	case event := <-sub.Events():
		if write(event) != nil {
			return
		}
	default:
	}
	heartbeat := time.NewTicker(10 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case event := <-coverage:
			if write(event) != nil {
				return
			}
		case <-b.Context().Done():
			_ = write(reviewEvent{Kind: "end"})
			return
		case <-r.Context().Done():
			return
		case <-sub.PreviewGap():
			_ = write(reviewEvent{Kind: "reset"})
			return
		case <-sub.Gap():
			_ = write(reviewEvent{Kind: "reset"})
			return
		case <-sub.Ready():
			for _, event := range testReview(b).TakePreviews(sub) {
				if write(event) != nil {
					return
				}
			}
		case event := <-sub.Events():
			if write(event) != nil {
				return
			}
		case <-heartbeat.C:
			if write(reviewEvent{Kind: "heartbeat"}) != nil {
				return
			}
		}
	}
}

func liveDiffRequest(ctx context.Context, connection liveDiffConnection, method string, body io.Reader) (*http.Request, error) {
	if !strings.HasPrefix(connection.Endpoint, "http://127.0.0.1:") {
		return nil, fmt.Errorf("invalid local live diff endpoint")
	}
	req, err := http.NewRequestWithContext(ctx, method, connection.Endpoint, body)
	if err == nil {
		req.Header.Set("Authorization", "Bearer "+connection.Token)
	}
	return req, err
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
