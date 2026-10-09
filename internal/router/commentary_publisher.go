package router

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	commentaryPublisherPath       = "/internal/commentary"
	commentaryOnceArgument        = "--journal-once"
	maxCommentaryRoutes           = 256
	journalListPageItems          = 4 // Worst-case escaped items stay below stock exec's 1 MiB collection cap.
	maxCommentaryEvents           = 1024
	maxCommentaryEventsPerRoute   = 64
	maxCommentaryPublicationBytes = 16 << 10
)

var (
	commentaryRouteTTL = time.Hour
	// Journal persistence can wait for an in-flight delivery in another router.
	// Let the stock worker's context own cancellation, not a shorter HTTP timer
	// that can report failure after the mutation has already been committed.
	commentaryHTTPClient = &http.Client{}
)

type publishedCommentary struct {
	callID    string
	messageID string
	text      string
}

type commentaryRoute struct {
	journalQuestion string
	finishReceipt   string
	finishTurn      string
	originThread    string
	sessionID       string
	callID          string
	expires         time.Time
	nextID          uint64
	events          []publishedCommentary
	complete        bool
}

type commentaryBroker struct {
	journalReader    func(context.Context, string, string, string, string, *int, string) ([]journalNode, error)
	journalPublisher func(context.Context, string, string, string, []journalMutation) ([]string, error)
	journalLister    func(context.Context, string, string, string) ([]journalItem, error)
	notice           func(string, string)
	debug            *debugOutput
	mu               sync.Mutex
	routes           map[string]*commentaryRoute
	eventCount       int
	closed           bool
}

func newCommentaryBroker() *commentaryBroker {
	return &commentaryBroker{
		routes: make(map[string]*commentaryRoute),
	}
}

func (b *commentaryBroker) capacityNotice() {
	if b.notice != nil {
		b.notice("journal_publisher_capacity", "Mekugi could not allocate a journal publisher: all 256 concurrent publisher routes are occupied. Existing work is unchanged.")
	}
}

func (b *commentaryBroker) subscribe(sessionID, callID string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cleanupExpiredLocked(time.Now())
	if b.closed || len(b.routes) >= maxCommentaryRoutes {
		if !b.closed {
			b.capacityNotice()
		}
		return ""
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return ""
	}
	token := base64.RawURLEncoding.EncodeToString([]byte(callID)) + "." + base64.RawURLEncoding.EncodeToString(random)
	b.routes[token] = &commentaryRoute{
		sessionID: sessionID,
		callID:    callID,
		expires:   time.Now().Add(commentaryRouteTTL),
	}
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
	// Oversized auxiliary text still reaches completion handling below.
	renderedFits := len(text) <= maxCommentaryPublicationBytes
	outcome := "accepted"
	switch {
	case strings.TrimSpace(text) == "":
		outcome = "blank"
	case !renderedFits:
		outcome = "oversized"
	case !withinRouteCapacity || b.eventCount >= maxCommentaryEvents:
		outcome = "capacity"
	}
	if (outcome == "capacity" || outcome == "oversized") && b.notice != nil {
		b.notice("progress_capacity", "Mekugi omitted auxiliary progress updates because its queue or message-size limit was reached (1,024 queued updates, 64 per publisher, 16 KiB per update). Tool execution and answers are unchanged; newer updates resume when the queue drains.")
	}
	messageID := ""
	if outcome == "accepted" {
		route.nextID++
		event := publishedCommentary{
			callID:    route.callID,
			messageID: commentaryMessageID(token + ":" + fmt.Sprint(route.nextID)),
			text:      text,
		}
		messageID = event.messageID
		route.events = append(route.events, event)
		b.eventCount++
	}
	// Empty completion signals are not authored progress. Only authenticated
	// publications reach this point; never log their bearer capability or text.
	if text != "" {
		source := "code_mode"
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
		if route.sessionID != sessionID {
			continue
		}
		events = append(events, b.drainLocked(token)...)
	}
	return events
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
		Journal jsonv1.RawMessage `json:"journal"`
		// ID is a publication receipt, never a journal operation operand.
		ReceiptID string `json:"id"`
		Complete  bool   `json:"complete"`
		Op        string `json:"op"`
		Agent     string `json:"agent"`
		P         string `json:"p"`
		Depth     *int   `json:"depth"`
		View      string `json:"view"`
		Page      *int   `json:"page"`
		Revision  string `json:"revision"`
	}
	decoder := jsonv1.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&publication); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		http.Error(writer, "invalid commentary publication", http.StatusBadRequest)
		return
	}
	if len(publication.Journal) == 0 && publication.Complete && publication.Op == "" && publication.ReceiptID == "" && publication.Agent == "" && publication.P == "" && publication.Depth == nil && publication.View == "" && publication.Page == nil && publication.Revision == "" {
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
	var session, thread, question, callID, finishReceipt, finishTurn string
	if route != nil {
		question, callID = route.journalQuestion, route.callID
		finishReceipt = route.finishReceipt
		finishTurn = route.finishTurn
		session, thread = route.sessionID, route.originThread
		route.expires = time.Now().Add(commentaryRouteTTL)
	}
	b.mu.Unlock()
	if route == nil {
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return
	}
	if b.debug != nil {
		started := time.Now()
		latency := new(journalLatency)
		request = request.WithContext(context.WithValue(request.Context(), journalLatencyKey{}, latency))
		writer = journalLatencyWriter{ResponseWriter: writer, latency: latency}
		defer latency.record(b.debug, thread, callID, started)
	}

	// Validate the complete operation before publishing any mutations.
	raw := bytes.TrimSpace(publication.Journal)
	switch publication.Op {
	case "", "batch":
		if publication.Complete || publication.Agent != "" || publication.P != "" || publication.Depth != nil || publication.View != "" || len(raw) == 0 || publication.ReceiptID == "" || publication.Page != nil || publication.Revision != "" {
			http.Error(writer, "invalid journal publication", http.StatusBadRequest)
			return
		}
	case "list", "read":
		if publication.Op == "list" && (publication.P != "" || publication.Depth != nil || publication.View != "") {
			http.Error(writer, "journal list accepts only agent", http.StatusBadRequest)
			return
		}
		if publication.Complete || publication.ReceiptID != "" || len(raw) != 0 {
			http.Error(writer, "journal "+publication.Op+" accepts only "+journalReadFields(publication.Op), http.StatusBadRequest)
			return
		}
		if publication.Page == nil && publication.Revision != "" || publication.Page != nil && (*publication.Page < 0 || *publication.Page > maxJournalItems || *publication.Page > 0 && publication.Revision == "") {
			http.Error(writer, "invalid journal "+publication.Op+" continuation", http.StatusBadRequest)
			return
		}
		if b.journalLister == nil {
			http.Error(writer, "journal lister unavailable", http.StatusBadRequest)
			return
		}
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
			writeJournalRejection(writer, b.debug, thread, callID, err)
			return
		}
	}
	mutations, finish, err := splitJournalFinish(mutations)
	if err == nil && finish && finishReceipt == "" {
		err = errors.New("journal finish requires a turn-bound host invocation")
	}
	if err != nil {
		writeJournalRejection(writer, b.debug, thread, callID, err)
		return
	}
	if finish {
		publication.ReceiptID = finishReceipt
		mutations = append(mutations, journalMutation{Op: "finish", finishTurn: finishTurn})
	}
	ids := []string{}
	if len(mutations) != 0 || finish {
		if b.journalPublisher == nil {
			http.Error(writer, journalPublisherUnavailable, http.StatusBadRequest)
			return
		}
		ids, err = b.journalPublisher(request.Context(), session, thread, publication.ReceiptID, bindJournalAnswers(mutations, question))
		if err != nil {
			writeJournalRejection(writer, b.debug, thread, callID, err)
			return
		}
		trace := featureUsageTrace{debug: b.debug, threadID: thread}
		trace.record("journal", "code_mode", "mutation", "accepted", callID, "")
	}
	if ids == nil {
		ids = []string{}
	}

	switch publication.Op {
	case "list", "read":
		var items []journalItem
		if publication.Op == "list" || b.journalReader == nil {
			items, err = b.journalLister(request.Context(), session, thread, publication.Agent)
		}
		if err != nil {
			http.Error(writer, "journal "+publication.Op+" rejected: "+err.Error(), http.StatusBadRequest)
			return
		}
		listed := []journalListItem{}
		if publication.Op == "list" {
			listed = make([]journalListItem, 0, len(items))
			for _, item := range items {
				listed = append(listed, journalListItem{
					ID: item.ID, Text: item.Text, Question: item.Question,
					Author: item.Author, Reported: item.Reported, Flushed: item.Flushed,
				})
			}
		}
		var payload any = listed
		if publication.Op == "read" {
			var nodes []journalNode
			var readErr error
			if b.journalReader != nil {
				nodes, readErr = b.journalReader(request.Context(), session, thread, publication.Agent, publication.P, publication.Depth, publication.View)
			} else {
				nodes, readErr = journalReadView(items, publication.P, publication.Depth, publication.View)
			}
			if readErr != nil {
				http.Error(writer, readErr.Error(), http.StatusBadRequest)
				return
			}
			flat := []journalNode{}
			var flatten func([]journalNode)
			flatten = func(nodes []journalNode) {
				for _, node := range nodes {
					children := node.Children
					node.Children = []journalNode{}
					flat = append(flat, node)
					flatten(children)
				}
			}
			flatten(nodes)
			payload = flat
		}
		response := map[string]any{"ok": true, "items": payload}
		if publication.Page != nil {
			encoded, err := jsonv1.Marshal(payload)
			if err != nil {
				http.Error(writer, "encode journal list", http.StatusInternalServerError)
				return
			}
			revision := fmt.Sprintf("%x", sha256.Sum256(encoded))
			if publication.Revision != "" && publication.Revision != revision {
				http.Error(writer, "journal changed during "+publication.Op+"; retry the read", http.StatusConflict)
				return
			}
			length := len(listed)
			if nodes, ok := payload.([]journalNode); ok {
				length = len(nodes)
			}
			start := min(*publication.Page, length)
			end := min(start+journalListPageItems, length)
			response["revision"] = revision
			if nodes, ok := payload.([]journalNode); ok {
				response["items"] = nodes[start:end]
			} else {
				response["items"] = listed[start:end]
			}
			if end < length {
				response["next"] = end
			}
		}
		writer.Header().Set("Content-Type", "application/json")
		encoder := jsonv1.NewEncoder(writer)
		encoder.SetEscapeHTML(false) // Match the journal store's protocol encoding.
		_ = encoder.Encode(response)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = jsonv1.NewEncoder(writer).Encode(map[string]any{"ok": true, "items": ids})
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

func (b *commentaryBroker) retireThread(thread string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for token, route := range b.routes {
		if route.originThread == thread {
			b.eventCount -= len(route.events)
			delete(b.routes, token)
		}
	}
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

type httpCommentarySink struct {
	endpoint string
	token    string
	client   *http.Client
}

func journalReadFields(op string) string {
	if op == "read" {
		return "op, p, agent, depth, and view"
	}
	return "op and agent"
}

func publishCommentaryOnce(ctx context.Context, writer io.Writer, arguments []string) (bool, error) {
	if len(arguments) != 4 && len(arguments) != 6 || arguments[0] != commentaryOnceArgument {
		return false, nil
	}
	text, err := url.PathUnescape(arguments[3])
	if err != nil {
		return true, err
	}
	sink := &httpCommentarySink{endpoint: arguments[1], token: arguments[2], client: commentaryHTTPClient}
	publication := map[string]any{"journal": jsonv1.RawMessage(text), "id": rand.Text()}
	var selector struct {
		Op string `json:"op"`
	}
	if json.Unmarshal([]byte(text), &selector) == nil && (selector.Op == "list" || selector.Op == "read") {
		var operation struct {
			Op    string `json:"op"`
			Agent string `json:"agent"`
			P     string `json:"p"`
			Depth *int   `json:"depth"`
			View  string `json:"view"`
		}
		if err := json.Unmarshal([]byte(text), &operation, json.RejectUnknownMembers(true)); err != nil {
			return true, fmt.Errorf("journal %s accepts only %s: %w", selector.Op, journalReadFields(selector.Op), err)
		}
		page, revision := 0, ""
		if len(arguments) == 6 {
			page, err = strconv.Atoi(arguments[4])
			if err != nil || page <= 0 || arguments[5] == "" {
				return true, fmt.Errorf("invalid journal %s continuation", operation.Op)
			}
			revision = arguments[5]
		}
		publication = map[string]any{"op": operation.Op, "agent": operation.Agent, "p": operation.P, "depth": operation.Depth, "view": operation.View, "page": page, "revision": revision}
	} else if len(arguments) != 4 {
		return true, fmt.Errorf("journal continuation requires read or list")
	}
	result, err := sink.send(ctx, publication)
	if err != nil {
		return true, err
	}
	_, err = writer.Write(result)
	return true, err
}

// A rejected mutation applied nothing and is the model's to correct. It is a
// result, not a transport failure, so the helper can report it without
// aborting the rest of the exec program.
func writeJournalRejection(writer http.ResponseWriter, debug *debugOutput, thread, callID string, err error) {
	trace := featureUsageTrace{debug: debug, threadID: thread}
	trace.record("journal", "code_mode", "mutation", "rejected", callID, "")
	writer.Header().Set("Content-Type", "application/json")
	_ = jsonv1.NewEncoder(writer).Encode(map[string]any{"ok": false, "error": "journal mutation rejected: " + err.Error()})
}

func (s *httpCommentarySink) send(ctx context.Context, publication map[string]any) ([]byte, error) {
	body, err := jsonv1.Marshal(publication)
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
		return nil, fmt.Errorf("journal response exceeds the %d-byte response limit; shorten journal items before retrying", maxJournalPublicationResponseBytes)
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusNoContent {
		detail := strings.TrimSpace(string(result))
		if len(detail) > maxCommentaryPublicationBytes {
			detail = detail[:maxCommentaryPublicationBytes] + " (error detail truncated)"
		}
		return nil, fmt.Errorf("journal request returned HTTP %d: %s", response.StatusCode, detail)
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
