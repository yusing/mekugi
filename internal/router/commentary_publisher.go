package router

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	commentaryPublisherPath       = "/internal/commentary"
	commentaryOnceArgument        = "--journal-once"
	maxThreadCommentaryIDs        = 16384
	maxCommentaryRoutes           = 256
	maxCommentaryEvents           = 1024
	maxCommentaryEventsPerRoute   = 64
	maxCommentaryPublicationBytes = 16 << 10
)

var (
	commentaryRouteTTL   = time.Hour
	commentaryHTTPClient = &http.Client{Timeout: 2 * time.Second}
)

type publishedCommentary struct {
	callID    string
	messageID string
	text      string
}

type commentaryRoute struct {
	journalQuestion string
	originThread    string
	author          string
	threadID        string
	sessionID       string
	callID          string
	expires         time.Time
	nextID          uint64
	events          []publishedCommentary
	complete        bool
}

// Replay provenance has its own non-evicting budget. A thread can outlive its
// route and change history sessions without losing user-only message identity.
// Its author is immutable: later requests cannot relabel an existing publisher.
// Exhaustion suppresses new commentary, never essential tool replay history.
type threadCommentaryProvenance struct {
	author    string
	sessionID string
	ids       map[string]struct{}
}

type commentaryBroker struct {
	journalPublisher func(context.Context, string, string, string, []journalMutation) ([]string, error)
	debug            *debugOutput
	activity         *subagentActivity
	threads          map[string]*threadCommentaryProvenance
	threadIDCount    int
	mu               sync.Mutex
	routes           map[string]*commentaryRoute
	eventCount       int
	closed           bool
}

func newCommentaryBroker() *commentaryBroker {
	return &commentaryBroker{routes: make(map[string]*commentaryRoute), threads: make(map[string]*threadCommentaryProvenance)}
}

func (b *commentaryBroker) subscribe(sessionID, callID, author string) string {
	if len(author) > maxCommentaryPublicationBytes || len(commentaryCode(author))+3 > maxCommentaryPublicationBytes {
		return ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cleanupExpiredLocked(time.Now())
	if b.closed || len(b.routes) >= maxCommentaryRoutes {
		return ""
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return ""
	}
	token := base64.RawURLEncoding.EncodeToString(random)
	b.routes[token] = &commentaryRoute{
		author:    author,
		sessionID: sessionID,
		callID:    callID,
		expires:   time.Now().Add(commentaryRouteTTL),
	}
	return token
}

// subscribeThread reuses the thread capability while refreshing its current replay session.
// The broker never calls back into the proxy: proxy locks may precede this lock.
func (b *commentaryBroker) subscribeThread(sessionID, threadID, author string) string {
	if sessionID == "" || threadID == "" || len(author) > maxCommentaryPublicationBytes || len(commentaryCode(author))+3 > maxCommentaryPublicationBytes {
		return ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	b.cleanupExpiredLocked(now)
	if b.closed {
		return ""
	}
	provenance := b.threads[threadID]
	if provenance == nil {
		if len(b.threads) >= maxCommentaryRoutes {
			return ""
		}
		provenance = &threadCommentaryProvenance{author: author, ids: make(map[string]struct{})}
		b.threads[threadID] = provenance
	}
	provenance.sessionID = sessionID
	for token, route := range b.routes {
		if route.threadID == threadID {
			route.sessionID = sessionID
			route.expires = now.Add(commentaryRouteTTL)
			return token
		}
	}
	if len(b.routes) >= maxCommentaryRoutes {
		return ""
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return ""
	}
	token := base64.RawURLEncoding.EncodeToString(random)
	b.routes[token] = &commentaryRoute{author: provenance.author, sessionID: sessionID, threadID: threadID, originThread: threadID, expires: now.Add(commentaryRouteTTL)}
	return token
}

func (b *commentaryBroker) publish(token, text string, complete bool) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	b.cleanupExpiredLocked(now)
	route := b.routes[token]
	if route == nil {
		return false
	}
	route.expires = now.Add(commentaryRouteTTL)
	withinRouteCapacity := route.nextID < maxCommentaryEventsPerRoute
	if route.threadID != "" {
		complete = false // Concurrent shell workers share this route; no worker owns its lifetime.
		withinRouteCapacity = len(route.events) < maxCommentaryEventsPerRoute && b.threadIDCount < maxThreadCommentaryIDs
	}
	// Check rendered bytes before attribution allocates or provenance is retained.
	// Oversized auxiliary text still reaches completion handling below.
	renderedFits := len(text) <= maxCommentaryPublicationBytes
	if route.author != "" && !hasCommentaryAuthor(text, route.author) {
		renderedFits = renderedFits && len(commentaryCode(route.author))+3 <= maxCommentaryPublicationBytes-len(text)
	}
	outcome := "accepted"
	switch {
	case strings.TrimSpace(text) == "":
		outcome = "blank"
	case !renderedFits:
		outcome = "oversized"
	case !withinRouteCapacity || b.eventCount >= maxCommentaryEvents:
		outcome = "capacity"
	}
	messageID := ""
	if outcome == "accepted" {
		route.nextID++
		event := publishedCommentary{
			callID:    route.callID,
			messageID: commentaryMessageID(token + ":" + fmt.Sprint(route.nextID)),
			text:      attributedCommentary(route.author, text),
		}
		messageID = event.messageID
		if route.threadID != "" {
			b.threads[route.threadID].ids[event.messageID] = struct{}{}
			b.threadIDCount++
		}
		b.activity.collect(route.originThread, event.messageID, "operation", event.text)
		route.events = append(route.events, event)
		b.eventCount++
	}
	// Empty completion signals are not authored progress. Only authenticated
	// publications reach this point; never log their bearer capability or text.
	if text != "" {
		source := "code_mode"
		if route.threadID != "" {
			source = "shell"
		}
		// The broker's sessionID is an internal workspace/thread replay key,
		// not a public routing session. Correlate with rendering by message ID.
		trace := featureUsageTrace{debug: b.debug, threadID: route.originThread}
		trace.record("commentary", source, "publication", outcome, route.callID, messageID)
	}
	if complete && len(route.events) == 0 {
		delete(b.routes, token)
		return true
	}
	route.complete = route.complete || complete
	return true
}

func (b *commentaryBroker) drainSession(sessionID, threadID string) []publishedCommentary {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cleanupExpiredLocked(time.Now())
	var events []publishedCommentary
	for token, route := range b.routes {
		if route.sessionID != sessionID || route.threadID != "" && route.threadID != threadID {
			continue
		}
		events = append(events, b.drainLocked(token)...)
	}
	return events
}

func (b *commentaryBroker) drainThreadSession(sessionID, threadID string) []publishedCommentary {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cleanupExpiredLocked(time.Now())
	var events []publishedCommentary
	for token, route := range b.routes {
		if route.threadID != "" && route.threadID == threadID && route.sessionID == sessionID {
			events = append(events, b.drainLocked(token)...)
		}
	}
	return events
}

func (b *commentaryBroker) hasThreadMessageID(threadID, messageID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	// A publication may already be claimed by a response when another request
	// remaps the thread's session. Its rendering authority is the stable thread.
	if thread := b.threads[threadID]; thread != nil {
		_, exists := thread.ids[messageID]
		return exists
	}
	return false
}

func (b *commentaryBroker) threadMessageIDs(sessionID string) map[string]struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	ids := make(map[string]struct{})
	for _, thread := range b.threads {
		if thread.sessionID == sessionID {
			maps.Copy(ids, thread.ids)
		}
	}
	return ids
}

func (b *commentaryBroker) cleanupExpiredLocked(now time.Time) {
	for token, route := range b.routes {
		if !now.Before(route.expires) {
			b.eventCount -= len(route.events)
			delete(b.routes, token)
		}
	}
}

func (b *commentaryBroker) close() {
	b.mu.Lock()
	b.closed = true
	clear(b.routes)
	clear(b.threads)
	b.threadIDCount = 0
	b.eventCount = 0
	b.mu.Unlock()
}

func (b *commentaryBroker) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	token, ok := strings.CutPrefix(request.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" {
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maxJournalFlushBytes*6)
	var publication struct {
		Journal  json.RawMessage `json:"journal"`
		ID       string          `json:"id"`
		Complete bool            `json:"complete"`
	}
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&publication); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		http.Error(writer, "invalid commentary publication", http.StatusBadRequest)
		return
	}
	if len(publication.Journal) == 0 && publication.Complete {
		if !b.publish(token, "", true) {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
		return
	}
	b.mu.Lock()
	b.cleanupExpiredLocked(time.Now())
	route := b.routes[token]
	var session, thread, question, callID string
	if route != nil {
		question, callID = route.journalQuestion, route.callID
		session, thread = route.sessionID, route.originThread
		route.expires = time.Now().Add(commentaryRouteTTL)
	}
	b.mu.Unlock()
	if route == nil {
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return
	}
	raw := bytes.TrimSpace(publication.Journal)
	if len(raw) != 0 && raw[0] == '{' {
		raw = append(append([]byte{'['}, raw...), ']')
	}
	mutations, err := decodeJournalMutations(raw)
	if err != nil || publication.ID == "" || b.journalPublisher == nil {
		http.Error(writer, "invalid journal publication", http.StatusBadRequest)
		return
	}
	ids, err := b.journalPublisher(request.Context(), session, thread, publication.ID, bindJournalAnswers(mutations, question))
	if err != nil {
		http.Error(writer, "journal mutation rejected", http.StatusBadRequest)
		return
	}
	source := "code_mode"
	if callID == "" {
		source = "shell"
	}
	trace := featureUsageTrace{debug: b.debug, threadID: thread}
	trace.record("journal", source, "mutation", "accepted", callID, "")

	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(map[string]any{"ok": true, "items": ids})
}

func commentaryPublisherURL(listenAddress string) (string, error) {
	host, port, err := net.SplitHostPort(listenAddress)
	if err != nil {
		return "", err
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return (&url.URL{Scheme: "http", Host: net.JoinHostPort(host, port), Path: commentaryPublisherPath}).String(), nil
}

func (b *commentaryBroker) drain(token string) []publishedCommentary {
	if token == "" {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cleanupExpiredLocked(time.Now())
	return b.drainLocked(token)
}

// drainLocked consumes publications without retiring a still-running publisher.
// Both live and deferred delivery share the same completion-sensitive lifetime.
func (b *commentaryBroker) drainLocked(token string) []publishedCommentary {
	route := b.routes[token]
	if route == nil {
		return nil
	}
	events := route.events
	b.eventCount -= len(events)
	route.events = nil
	if route.complete {
		delete(b.routes, token)
	}
	return events
}

func (b *commentaryBroker) cancel(token string) {
	if token == "" {
		return
	}
	b.mu.Lock()
	if route := b.routes[token]; route != nil {
		b.eventCount -= len(route.events)
		delete(b.routes, token)
	}
	b.mu.Unlock()
}

type shellCommentarySink interface {
	Publish(context.Context, string) error
	Complete(context.Context) error
}

type httpShellCommentarySink struct {
	endpoint string
	token    string
	result   []byte
	client   *http.Client
}

func publishCommentaryOnce(ctx context.Context, writer io.Writer, arguments []string) (bool, error) {
	if len(arguments) != 4 || arguments[0] != commentaryOnceArgument {
		return false, nil
	}
	text, err := url.PathUnescape(arguments[3])
	if err != nil {
		return true, err
	}
	sink := &httpShellCommentarySink{endpoint: arguments[1], token: arguments[2], client: commentaryHTTPClient}
	if err := sink.Publish(ctx, text); err != nil {
		return true, err
	}
	_, err = writer.Write(sink.result)
	return true, err
}

func (s *httpShellCommentarySink) Publish(ctx context.Context, text string) error {
	return s.send(ctx, map[string]any{"journal": json.RawMessage(text), "id": rand.Text()})
}

func (s *httpShellCommentarySink) Complete(ctx context.Context) error {
	return s.send(ctx, map[string]any{"complete": true})
}

func (s *httpShellCommentarySink) send(ctx context.Context, publication map[string]any) error {
	body, err := json.Marshal(publication)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+s.token)
	request.Header.Set("Content-Type", "application/json")
	response, err := s.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	s.result, err = io.ReadAll(io.LimitReader(response.Body, 16<<10))
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("journal publisher returned status %d", response.StatusCode)
	}
	return nil
}

// Only a call-scoped capability can pin the request's user message.
func (b *commentaryBroker) bindJournalQuestion(token, question string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if route := b.routes[token]; route != nil && route.callID != "" {
		route.journalQuestion = question
	}
}

func (b *commentaryBroker) bindActivity(token, thread string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if route := b.routes[token]; route != nil && route.originThread == "" {
		route.originThread = thread
	}
}
