package router

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestJournalPublisherPreservesUnderlyingError(t *testing.T) {
	broker := newCommentaryBroker()
	broker.journalPublisher = func(context.Context, string, string, string, []journalMutation) ([]string, error) {
		return nil, errors.New("journal item limit is 256; delete obsolete items")
	}
	broker.journalLister = func(context.Context, string, string, string) ([]journalItem, error) {
		return nil, errors.New("journal state is missing; retry initialization")
	}
	server := httptest.NewServer(http.HandlerFunc(broker.serveHTTP))
	defer server.Close()
	token := broker.subscribeThread("/w\x00thread", "thread", "")
	sink := &httpCommentarySink{endpoint: server.URL, token: token, client: server.Client()}
	for _, publication := range []map[string]any{
		{"id": "fixture-add", "journal": []journalMutation{{Op: "add", Text: new("milestone")}}},
		{"op": "list"},
	} {
		_, err := sink.send(t.Context(), publication)
		if err == nil || !strings.Contains(err.Error(), "HTTP 400") ||
			!strings.Contains(err.Error(), "delete obsolete items") && !strings.Contains(err.Error(), "retry initialization") {
			t.Fatalf("underlying publisher error lost: %v", err)
		}
	}
}

func TestCapacityNoticesAreVisibleAndDoNotConsumeTools(t *testing.T) {
	issues := NewCriticalErrors()
	broker := newCommentaryBroker()
	broker.notice = func(category, message string) { issues.addNotice("", category, message) }
	for range maxCommentaryRoutes {
		if broker.subscribe("session", "call", "") == "" {
			t.Fatal("publisher rejected before concurrency limit")
		}
	}
	if broker.subscribe("session", "overflow", "") != "" {
		t.Fatal("publisher accepted beyond concurrency limit")
	}
	visible := issues.transform("any-root", false)
	if len(visible.messages) != 1 {
		t.Fatal("capacity notice is not available to the root")
	}
	body, err := visible.TransformJSON([]byte(`{"status":"completed","output":[{"type":"message","content":[]}]}`))
	if err != nil || !strings.Contains(string(body), "256 concurrent publisher routes") ||
		!strings.Contains(string(body), "direct functions.journal remains available") {
		t.Fatalf("capacity diagnostic: %s %v", body, err)
	}
}

func TestReadCapacityErrorIncludesLimitAndRemedy(t *testing.T) {
	err := validateReadRecord(shellOutputRecord{Stdout: strings.Repeat("x", maxShellOutputBytes+1)})
	if err == nil || !strings.Contains(err.Error(), "limit is 16777216 bytes") || !strings.Contains(err.Error(), "split the operation") {
		t.Fatalf("missing capacity diagnostic: %v", err)
	}
}
