package router

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
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
	shellCall       bool
	finishReceipt   string
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
	journalLister    func(context.Context, string, string, string) ([]journalItem, error)
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
	return &commentaryBroker{
		routes:  make(map[string]*commentaryRoute),
		threads: make(map[string]*threadCommentaryProvenance),
	}
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
		Journal json.RawMessage `json:"journal"`
		// ID is a publication receipt, never a journal operation operand.
		ReceiptID string `json:"id"`
		Complete  bool   `json:"complete"`
		Op        string `json:"op"`
		Agent     string `json:"agent"`
	}
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&publication); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		http.Error(writer, "invalid commentary publication", http.StatusBadRequest)
		return
	}
	if len(publication.Journal) == 0 && publication.Complete && publication.Op == "" && publication.ReceiptID == "" && publication.Agent == "" {
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
	var session, thread, question, callID, finishReceipt string
	var shellCall bool
	if route != nil {
		shellCall = route.shellCall
		question, callID, finishReceipt = route.journalQuestion, route.callID, route.finishReceipt
		session, thread = route.sessionID, route.originThread
		route.expires = time.Now().Add(commentaryRouteTTL)
	}
	b.mu.Unlock()
	if route == nil {
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Validate the complete operation before publishing any mutations.
	raw := bytes.TrimSpace(publication.Journal)
	switch publication.Op {
	case "", "batch":
		if publication.Complete || publication.Agent != "" || len(raw) == 0 || publication.ReceiptID == "" {
			http.Error(writer, "invalid journal publication", http.StatusBadRequest)
			return
		}
	case "list":
		if publication.Complete || publication.ReceiptID != "" || len(raw) != 0 {
			http.Error(writer, "journal list accepts only agent", http.StatusBadRequest)
			return
		}
		if b.journalLister == nil {
			http.Error(writer, "journal lister unavailable", http.StatusBadRequest)
			return
		}
	case "finish":
		if publication.Complete || publication.Agent != "" || finishReceipt == "" {
			http.Error(writer, "journal finish requires a turn-bound shell invocation", http.StatusBadRequest)
			return
		}
		publication.ReceiptID = finishReceipt
	default:
		http.Error(writer, "unknown journal operation", http.StatusBadRequest)
		return
	}
	if len(raw) != 0 && raw[0] == '{' {
		raw = append(append([]byte{'['}, raw...), ']')
	}
	var mutations []journalMutation
	var err error
	if len(raw) != 0 {
		mutations, err = decodeJournalMutations(raw)
		if err != nil {
			http.Error(writer, "invalid journal publication", http.StatusBadRequest)
			return
		}
	}
	ids := []string{}
	if len(mutations) != 0 || publication.Op == "finish" {
		if b.journalPublisher == nil {
			http.Error(writer, "journal publisher unavailable", http.StatusBadRequest)
			return
		}
		ids, err = b.journalPublisher(request.Context(), session, thread, publication.ReceiptID, bindJournalAnswers(mutations, question))
		if err != nil {
			http.Error(writer, "journal mutation rejected", http.StatusBadRequest)
			return
		}
		source := "code_mode"
		if callID == "" || shellCall {
			source = "shell"
		}
		trace := featureUsageTrace{debug: b.debug, threadID: thread}
		trace.record("journal", source, "mutation", "accepted", callID, "")
	}

	switch publication.Op {
	case "list":
		var items []journalItem
		items, err = b.journalLister(request.Context(), session, thread, publication.Agent)
		if err != nil {
			http.Error(writer, "journal list rejected", http.StatusBadRequest)
			return
		}
		listed := make([]journalListItem, 0, len(items))
		for _, item := range items {
			listed = append(listed, journalListItem{
				ID: item.ID, Text: item.Text, Question: item.Question,
				Author: item.Author, Reported: item.Reported, Flushed: item.Flushed,
			})
		}
		writer.Header().Set("Content-Type", "application/json")
		encoder := json.NewEncoder(writer)
		encoder.SetEscapeHTML(false) // Match the journal store's protocol encoding.
		_ = encoder.Encode(map[string]any{"ok": true, "items": listed})
		return
	case "finish":
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{"ok": true, "finish_requested": true, "journal_ids": ids})
		return
	}
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
	RequestJournal(context.Context, shellJournalCommand) (shellJournalResult, error)
	Publish(context.Context, string) error
	Complete(context.Context) error
}

type httpShellCommentarySink struct {
	endpoint string
	token    string
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
	result, err := sink.send(ctx, map[string]any{"journal": json.RawMessage(text), "id": rand.Text()})
	if err != nil {
		return true, err
	}
	_, err = writer.Write(result)
	return true, err
}

func (s *httpShellCommentarySink) Publish(ctx context.Context, text string) error {
	_, err := s.send(ctx, map[string]any{"journal": json.RawMessage(text), "id": rand.Text()})
	return err
}

func (s *httpShellCommentarySink) RequestJournal(ctx context.Context, command shellJournalCommand) (shellJournalResult, error) {
	publication := make(map[string]any)
	if command.Op != "list" && command.Op != "finish" {
		publication["id"] = rand.Text()
	}
	switch command.Op {
	case "list":
		publication["op"] = "list"
		if command.Agent != "" {
			publication["agent"] = command.Agent
		}
	case "finish":
		if len(command.Batch) != 0 {
			publication["journal"] = command.Batch
		}
		publication["op"] = "finish"
	case "batch":
		publication["op"] = "batch"
		publication["journal"] = command.Batch
	case "add", "edit", "delete":
		if command.Mutation == nil {
			return shellJournalResult{}, errors.New("journal mutation is missing")
		}
		publication["journal"] = []journalMutation{*command.Mutation}
	default:
		return shellJournalResult{}, errors.New("unknown journal operation")
	}
	resultBytes, err := s.send(ctx, publication)
	if err != nil {
		return shellJournalResult{}, err
	}
	var response struct {
		OK              bool            `json:"ok"`
		Items           json.RawMessage `json:"items"`
		JournalIDs      []string        `json:"journal_ids"`
		FinishRequested bool            `json:"finish_requested"`
	}
	if err := json.Unmarshal(resultBytes, &response); err != nil || !response.OK {
		return shellJournalResult{}, errors.New("journal publisher returned an invalid result")
	}
	result := shellJournalResult{FinishRequested: response.FinishRequested}
	if command.Op == "list" {
		if err := json.Unmarshal(response.Items, &result.Items); err != nil {
			return shellJournalResult{}, errors.New("journal publisher returned invalid list items")
		}
		return result, nil
	}
	if len(response.Items) != 0 {
		if err := json.Unmarshal(response.Items, &result.IDs); err != nil {
			return shellJournalResult{}, errors.New("journal publisher returned invalid journal IDs")
		}
	} else {
		result.IDs = response.JournalIDs
	}
	return result, nil
}

func (s *httpShellCommentarySink) Complete(ctx context.Context) error {
	_, err := s.send(ctx, map[string]any{"complete": true})
	return err
}

func (s *httpShellCommentarySink) send(ctx context.Context, publication map[string]any) ([]byte, error) {
	body, err := json.Marshal(publication)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+s.token)
	request.Header.Set("Content-Type", "application/json")
	response, err := s.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	result, err := io.ReadAll(io.LimitReader(response.Body, maxJournalPublicationResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(result) > maxJournalPublicationResponseBytes {
		return nil, errors.New("journal publisher result exceeds the response budget")
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusNoContent {
		return nil, fmt.Errorf("journal publisher returned status %d", response.StatusCode)
	}
	return result, nil
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
