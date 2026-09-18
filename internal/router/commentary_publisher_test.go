package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func TestJournalPublisherAuthenticatesAndMutatesThreadStore(t *testing.T) {
	broker := newCommentaryBroker()
	t.Cleanup(broker.close)
	store := newJournalStore()
	if err := store.initialize(t.Context(), nil, "workspace", "thread", "/root", ""); err != nil {
		t.Fatal(err)
	}
	broker.journalPublisher = func(ctx context.Context, _, thread, receipt string, mutations []journalMutation) ([]string, error) {
		return store.apply(ctx, nil, "workspace", thread, receipt, mutations)
	}
	server := httptest.NewServer(http.HandlerFunc(broker.serveHTTP))
	t.Cleanup(server.Close)
	token := broker.subscribe("session", "call-live", "")
	broker.bindActivity(token, "thread")
	sink := &httpShellCommentarySink{endpoint: server.URL, token: token, client: server.Client()}
	for _, mutation := range []string{
		`{"op":"add","text":"Initial milestone"}`,
		`{"op":"edit","id":"amber","text":"Verified milestone","report_now":true}`,
	} {
		if err := sink.Publish(t.Context(), mutation); err != nil {
			t.Fatal(err)
		}
	}
	items, err := store.list(t.Context(), nil, "workspace", "thread")
	if err != nil || len(items) != 1 || items[0].Text != "Verified milestone" || !items[0].ReportNow || items[0].Reported {
		t.Fatalf("runtime mutations: %+v %v", items, err)
	}
	sink.token = "unknown-capability"
	if err := sink.Publish(t.Context(), `{"op":"delete","id":"amber"}`); err == nil {
		t.Fatal("unauthenticated journal publication succeeded")
	}
	sink.token = token
	if err := sink.Publish(t.Context(), `{"op":"delete","id":"amber"}`); err != nil {
		t.Fatal(err)
	}
	if err := sink.Complete(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := sink.Publish(t.Context(), `{"op":"add","text":"After completion"}`); err == nil {
		t.Fatal("completed capability accepted another mutation")
	}
	items, err = store.list(t.Context(), nil, "workspace", "thread")
	if err != nil || len(items) != 0 {
		t.Fatalf("runtime delete: %+v %v", items, err)
	}
}

func TestShellJournalRequesterSupportsListAnswersBatchAndFinish(t *testing.T) {
	broker := newCommentaryBroker()
	t.Cleanup(broker.close)
	store := newJournalStore()
	if err := store.initialize(t.Context(), nil, "workspace", "thread", "/root", ""); err != nil {
		t.Fatal(err)
	}
	broker.journalPublisher = func(ctx context.Context, session, thread, receipt string, mutations []journalMutation) ([]string, error) {
		workspace, _, ok := strings.Cut(session, "\x00")
		if !ok {
			return nil, errors.New("missing workspace")
		}
		return store.apply(ctx, nil, workspace, thread, receipt, mutations)
	}
	broker.journalLister = func(ctx context.Context, session, thread, agent string) ([]journalItem, error) {
		workspace, _, ok := strings.Cut(session, "\x00")
		if !ok {
			return nil, errors.New("missing workspace")
		}
		if agent != "" {
			return store.listAgent(ctx, nil, workspace, thread, agent)
		}
		return store.list(ctx, nil, workspace, thread)
	}
	server := httptest.NewServer(http.HandlerFunc(broker.serveHTTP))
	t.Cleanup(server.Close)
	token := broker.subscribe("workspace\x00session", "call-shell", "/root")
	broker.bindActivity(token, "thread")
	broker.bindJournalQuestion(token, "What changed?")
	broker.routes[token].finishReceipt = shellJournalFinishReceipt("turn", "call-shell")
	sink := &httpShellCommentarySink{endpoint: server.URL, token: token, client: server.Client()}

	result, err := sink.RequestJournal(t.Context(), shellJournalCommand{
		Op: "add", Mutation: &journalMutation{Op: "add", Text: new("Answer"), Answer: new(true)},
	})
	if err != nil || len(result.IDs) != 1 || result.IDs[0] != "amber" {
		t.Fatalf("answer add = %+v, %v", result, err)
	}
	result, err = sink.RequestJournal(t.Context(), shellJournalCommand{
		Op: "batch",
		Batch: []journalMutation{
			{Op: "add", Text: new("Second")},
			{Op: "edit", ID: "amber", Text: new("Updated")},
		},
	})
	if err != nil || len(result.IDs) != 2 {
		t.Fatalf("batch = %+v, %v", result, err)
	}
	result, err = sink.RequestJournal(t.Context(), shellJournalCommand{Op: "list", Agent: ""})
	if err != nil || len(result.Items) != 2 || result.Items[0].Question != "What changed?" || result.Items[0].Text != "Updated" {
		t.Fatalf("list = %+v, %v", result, err)
	}
	result, err = sink.RequestJournal(t.Context(), shellJournalCommand{Op: "finish", Batch: []journalMutation{{Op: "add", Text: new("Final")}}})
	if err != nil || !result.FinishRequested || len(result.IDs) != 1 || result.IDs[0] != "arch" {
		t.Fatalf("finish = %+v, %v", result, err)
	}
}
func TestCommentaryDrainRetainsActiveCapacityUntilCompletionOrExpiry(t *testing.T) {
	for _, mode := range []string{"token", "session"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				broker := newCommentaryBroker()
				t.Cleanup(broker.close)
				drain := func(token string) []publishedCommentary {
					if mode == "token" {
						return broker.drain(token)
					}
					return broker.drainSession("session", "")
				}
				tokens := make([]string, maxCommentaryRoutes)
				for i := range tokens {
					tokens[i] = broker.subscribe("session", "call", "")
					if tokens[i] == "" || !broker.publish(tokens[i], "first", false) {
						t.Fatal("route capacity was not available")
					}
				}
				seen := make(map[string]bool)
				for _, token := range tokens {
					for _, event := range drain(token) {
						if seen[event.messageID] {
							t.Fatal("publication delivered twice")
						}
						seen[event.messageID] = true
					}
				}
				if len(seen) != maxCommentaryRoutes || broker.eventCount != 0 || broker.subscribe("session", "overflow", "") != "" {
					t.Fatalf("active drain: delivered=%d pending=%d routes=%d", len(seen), broker.eventCount, len(broker.routes))
				}
				if !broker.publish(tokens[0], "second", true) {
					t.Fatal("drain retired an active publisher")
				}
				events := drain(tokens[0])
				if len(events) != 1 || seen[events[0].messageID] || events[0].text != "second" || broker.publish(tokens[0], "late", false) {
					t.Fatalf("completed drain = %+v", events)
				}
				replacement := broker.subscribe("session", "replacement", "")
				if replacement == "" || !broker.publish(replacement, "pending expiry", false) {
					t.Fatal("completion did not release capacity")
				}
				time.Sleep(commentaryRouteTTL)
				if events := drain(replacement); len(events) != 0 || broker.eventCount != 0 || len(broker.routes) != 0 {
					t.Fatalf("expiry: events=%+v pending=%d routes=%d", events, broker.eventCount, len(broker.routes))
				}
				if broker.subscribe("session", "after-expiry", "") == "" {
					t.Fatal("expiry did not release route capacity")
				}
			})
		})
	}
}

func TestPublishJournalOnceReportsPublicationFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)

	handled, err := publishCommentaryOnce(t.Context(), io.Discard, []string{
		commentaryOnceArgument, server.URL, "token", `%7B%22op%22%3A%22add%22%2C%22text%22%3A%22Working%22%7D`,
	})
	if !handled || err == nil {
		t.Fatalf("handled = %v, error = %v", handled, err)
	}
	if handled, err := publishCommentaryOnce(t.Context(), io.Discard, []string{
		commentaryOnceArgument, server.URL, "token", "%zz",
	}); !handled || err == nil {
		t.Fatalf("invalid escape handled = %v, error = %v", handled, err)
	}
	if handled, err := publishCommentaryOnce(t.Context(), io.Discard, []string{"other"}); handled || err != nil {
		t.Fatalf("unrelated invocation handled = %v, error = %v", handled, err)
	}
}

func TestConcurrentSessionDrainsOnlyOriginatingShellCommentary(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "request", true: "terminal"}[terminal], func(t *testing.T) {
			proxy := newManagedMekugiProxy(t)
			const sessionID = "session"
			for range 2 {
				if err := proxy.activateSession(sessionID); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { proxy.deactivateSession(sessionID) })
			}
			shell := proxy.commentary.subscribeThread(sessionID, "root", "")
			child := proxy.commentary.subscribeThread(sessionID, "child", "/root/child")
			call := proxy.commentary.subscribe(sessionID, "code-mode-call", "")
			for token, text := range map[string]string{shell: "root progress", child: "child progress", call: "call progress"} {
				if token == "" || !proxy.commentary.publish(token, text, false) {
					t.Fatal("commentary was not published")
				}
			}
			drain := proxy.drainCommentarySession
			if terminal {
				drain = proxy.drainThreadCommentarySession
			}
			// Concurrent responses for the same thread compete atomically. Other
			// threads and call-scoped deferred publications cannot be consumed.
			results := make(chan []publishedCommentary, 2)
			var workers sync.WaitGroup
			for range 2 {
				workers.Go(func() { results <- drain(sessionID, "root") })
			}
			workers.Wait()
			close(results)
			var events []publishedCommentary
			for result := range results {
				events = append(events, result...)
			}
			if len(events) != 1 || events[0].text != "root progress" {
				t.Fatalf("concurrent root delivery = %+v", events)
			}
			if events := drain(sessionID, "child"); len(events) != 1 || !strings.Contains(events[0].text, "child progress") {
				t.Fatalf("child delivery = %+v", events)
			}
			if !proxy.commentary.publish(shell, "later root progress", false) {
				t.Fatal("drain retired the shell publisher")
			}
			if events := drain(sessionID, "root"); len(events) != 1 || events[0].text != "later root progress" {
				t.Fatalf("later delivery = %+v", events)
			}
			proxy.deactivateSession(sessionID)
			if events := proxy.drainCommentarySession(sessionID, "root"); len(events) != 1 || events[0].text != "call progress" {
				t.Fatalf("non-concurrent call delivery = %+v", events)
			}
		})
	}
}

func TestShellRouteKeepsCleanCommandWithoutDefaultCommentary(t *testing.T) {
	for _, test := range []struct {
		name  string
		input string
	}{
		{name: "ordinary shell", input: "printf ok"},
		{name: "commentary", input: "commentary Running check\nprintf ok"},
	} {
		t.Run(test.name, func(t *testing.T) {
			transform, proxy, _, _ := newMekugiTestTransform(t)
			proxy.commentaryEndpoint = "http://127.0.0.1:8080" + commentaryPublisherPath
			response, err := transform.TransformJSON(mustTestJSON(t, map[string]any{
				"status": "completed", "output": []any{map[string]any{
					"type": "custom_tool_call", "id": "item-shell", "call_id": "call-shell",
					"name": "shell", "input": test.input, "status": "completed",
				}},
			}))
			if err != nil {
				t.Fatal(err)
			}
			var decoded struct {
				Output []map[string]json.RawMessage `json:"output"`
			}
			if json.Unmarshal(response, &decoded) != nil || len(decoded.Output) != 1 {
				t.Fatalf("shell response = %s", response)
			}
			carrier := jsonString(decoded.Output[0], "input")
			var args struct {
				Command string `json:"cmd"`
			}
			decodeExecCarrierArguments(t, carrier, &args)
			want := "shell bash " + shellQuoteArgument((shellInvocation{JournalToken: transform.commentarySubscriptions[0].token}).source(test.input))
			if args.Command != want {
				t.Fatalf("command = %q, want %q", args.Command, want)
			}

			if strings.Contains(carrier, "--commentary-") || strings.Contains(carrier, proxy.commentaryEndpoint) || len(transform.commentarySubscriptions) != 1 {
				t.Fatalf("shell carrier = %s", carrier)
			}
		})
	}
}

func TestShellCommentaryPreservesDirectCommands(t *testing.T) {
	for _, native := range []bool{false, true} {
		name := "code mode"
		if native {
			name = "native"
		}
		t.Run(name, func(t *testing.T) {
			var transform *mekugiResponseTransform
			var proxy *mekugiProxy
			if native {
				proxy = newManagedMekugiProxy(t)
				transform, _ = newNativeMekugiTestTransformWithProxy(t, proxy)
			} else {
				transform, proxy, _, _ = newMekugiTestTransform(t)
			}
			proxy.commentaryEndpoint = "http://127.0.0.1:8080" + commentaryPublisherPath
			const command = "mktemp -d -t mekugi-shell.XXXXXXXXXX"
			response, err := transform.TransformJSON(mustTestJSON(t, map[string]any{
				"status": "completed", "output": []any{map[string]any{
					"type": "custom_tool_call", "id": "item-shell", "call_id": "call-shell",
					"name": "shell", "input": command, "status": "completed",
				}},
			}))
			if err != nil {
				t.Fatal(err)
			}
			var decoded struct {
				Output []map[string]json.RawMessage `json:"output"`
			}
			if err := json.Unmarshal(response, &decoded); err != nil {
				t.Fatal(err)
			}
			if len(decoded.Output) != 1 {
				t.Fatalf("output count = %d, want 1", len(decoded.Output))
			}
			var arguments struct {
				Command string `json:"cmd"`
			}
			if native {
				if err := json.Unmarshal([]byte(jsonString(decoded.Output[0], "arguments")), &arguments); err != nil {
					t.Fatal(err)
				}
			} else {
				decodeExecCarrierArguments(t, jsonString(decoded.Output[0], "input"), &arguments)
			}
			visibleCommand := arguments.Command
			if native {
				// Native carriers append the existing script-retention result metadata.
				visibleCommand, _, _ = strings.Cut(visibleCommand, "\n")
			}
			if visibleCommand != command {
				t.Fatal("commentary changed the direct command")
			}
			if len(transform.commentarySubscriptions) != 0 {
				t.Fatal("direct command allocated a commentary subscription")
			}
		})
	}
}

func shellCommentaryTestItem() map[string]any {
	return map[string]any{
		"type": "custom_tool_call", "id": "item-runtime", "call_id": "call-runtime",
		"name": "exec", "input": `await journal({op: "add", text: "Working"});`, "status": "completed",
	}
}

func newRuntimeCommentaryTransform(t *testing.T) (*mekugiResponseTransform, *mekugiProxy) {
	t.Helper()
	transform, proxy, _, _ := newMekugiTestTransform(t)
	proxy.commentaryEndpoint = "http://127.0.0.1:8080" + commentaryPublisherPath
	return transform, proxy
}

func runtimeCommentaryToken(t *testing.T, transform *mekugiResponseTransform) string {
	t.Helper()
	if len(transform.commentarySubscriptions) != 1 {
		t.Fatalf("commentary subscription count = %d", len(transform.commentarySubscriptions))
	}
	return transform.commentarySubscriptions[0].token
}

func TestReadyRuntimeCommentaryPrecedesEveryStreamTerminal(t *testing.T) {
	for _, status := range []string{"completed", "failed", "incomplete"} {
		t.Run(status, func(t *testing.T) {
			transform, proxy := newRuntimeCommentaryTransform(t)
			carrier, err := transform.TransformSSE(mustTestJSON(t, map[string]any{
				"type": "response.output_item.done", "item": shellCommentaryTestItem(),
			}))
			if err != nil || len(carrier) != 1 || !bytes.Contains(carrier[0], []byte(`"name":"exec"`)) {
				t.Fatalf("shell carrier = %s, %v", carrier, err)
			}
			token := runtimeCommentaryToken(t, transform)
			if !proxy.commentary.publish(token, "Running streamed work.", false) {
				t.Fatal("runtime commentary was not published")
			}
			beforeBytes := proxy.historyBytes
			payload := mustTestJSON(t, map[string]any{
				"type":     "response." + status,
				"response": map[string]any{"status": status, "output": []any{}},
			})
			events, err := transform.TransformSSE(payload)
			if err != nil || len(events) != 2 || !bytes.Contains(events[0], []byte("Running streamed work.")) ||
				!bytes.Contains(events[1], []byte(`"type":"response.`+status+`"`)) {
				t.Fatalf("terminal events = %s, %v", events, err)
			}
			history, exists := proxy.history(transform.historySessionID, "call-runtime")
			if !exists || len(history.CommentaryMessageIDs) != 1 || proxy.historyBytes-beforeBytes != len(history.CommentaryMessageIDs[0]) {
				t.Fatalf("history = %+v, bytes before = %d, after = %d", history, beforeBytes, proxy.historyBytes)
			}
			transform.Close()
			if !proxy.commentary.publish(token, "Later work.", false) {
				t.Fatal("terminal response retired an active publisher")
			}
			deferred := proxy.drainCommentarySession(transform.historySessionID, transform.shellThreadID)
			if len(deferred) != 1 || deferred[0].text != "Later work." || deferred[0].messageID == history.CommentaryMessageIDs[0] {
				t.Fatalf("deferred events = %+v", deferred)
			}
			if len(proxy.drainCommentarySession(transform.historySessionID, transform.shellThreadID)) != 0 {
				t.Fatal("publication delivered twice")
			}
			if !proxy.commentary.publish(token, "", true) || proxy.commentary.publish(token, "after completion", false) {
				t.Fatal("publisher completion did not retire drained route")
			}
		})
	}
}

func TestJSONTerminalHandsOffRuntimePublisher(t *testing.T) {
	for _, status := range []string{"completed", "failed", "incomplete"} {
		t.Run(status, func(t *testing.T) {
			transform, proxy := newRuntimeCommentaryTransform(t)
			payload, err := transform.TransformJSON(mustTestJSON(t, map[string]any{
				"status": status, "output": []any{shellCommentaryTestItem()},
			}))
			if err != nil || !bytes.Contains(payload, []byte(`"name":"exec"`)) {
				t.Fatalf("JSON carrier = %s, %v", payload, err)
			}
			token := runtimeCommentaryToken(t, transform)
			transform.Close()
			if !proxy.commentary.publish(token, "Deferred JSON work.", true) {
				t.Fatal("JSON terminal cancelled handed-off publisher")
			}
			if events := proxy.drainCommentarySession(transform.historySessionID, transform.shellThreadID); len(events) != 1 || events[0].text != "Deferred JSON work." {
				t.Fatalf("deferred JSON events = %+v", events)
			}
		})
	}
}

func TestEarlyStreamReleasePreservesHandedOffPublishers(t *testing.T) {
	for _, name := range []string{"exec"} {
		t.Run(name, func(t *testing.T) {
			transform, proxy := newRuntimeCommentaryTransform(t)
			item := shellCommentaryTestItem()
			item["name"], item["input"], item["status"] = name, "", "in_progress"
			if _, err := transform.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.output_item.added", "item": item})); err != nil {
				t.Fatal(err)
			}
			input := "printf ok"
			if name == "exec" {
				input = `await journal({op: "add", text: "Working"});`
			}
			events, err := transform.TransformSSE(mustTestJSON(t, map[string]any{
				"type": "response.custom_tool_call_input.done", "item_id": "item-runtime", "input": input,
			}))
			if err != nil || len(events) == 0 || !bytes.Contains(bytes.Join(events, nil), []byte("response.custom_tool_call_input.done")) {
				t.Fatalf("carrier input handoff = %s, %v", events, err)
			}
			token := runtimeCommentaryToken(t, transform)
			transform.Close() // A disconnect can occur before item.done or a terminal.
			if !proxy.commentary.publish(token, "Work after disconnect.", true) {
				t.Fatal("disconnect cancelled an emitted carrier publisher")
			}
			if events := proxy.drainCommentarySession(transform.historySessionID, transform.shellThreadID); len(events) != 1 || events[0].text != "Work after disconnect." {
				t.Fatalf("deferred disconnected events = %+v", events)
			}
			if _, exists := proxy.history(transform.historySessionID, "call-runtime"); !exists {
				t.Fatal("emitted carrier has no replay history for deferred commentary")
			}
		})
	}
}

func TestUnhandedRuntimeCommentaryRouteIsCancelled(t *testing.T) {
	transform, proxy := newRuntimeCommentaryTransform(t)
	_, err := transform.TransformJSON(mustTestJSON(t, map[string]any{
		"status": "completed", "output": []any{
			shellCommentaryTestItem(),
			map[string]any{"type": "custom_tool_call", "name": "shell", "input": testShellEditSource},
		},
	}))
	if err == nil {
		t.Fatal("malformed later call did not prevent JSON handoff")
	}
	token := runtimeCommentaryToken(t, transform)
	if !proxy.commentary.publish(token, "Unhanded work.", false) {
		t.Fatal("prepared route was not registered")
	}
	transform.Close()
	if proxy.commentary.publish(token, "later", false) || len(proxy.drainCommentarySession(transform.historySessionID, transform.shellThreadID)) != 0 {
		t.Fatal("unhanded route or its queued publication was retained")
	}
}

func TestShellJournalPublisherValidatesBeforeMutation(t *testing.T) {
	broker := newCommentaryBroker()
	t.Cleanup(broker.close)
	calls := 0
	broker.journalPublisher = func(_ context.Context, _, _, _ string, _ []journalMutation) ([]string, error) {
		calls++
		return []string{"amber"}, nil
	}
	token := broker.subscribeThread("workspace\x00thread", "thread", "/root")
	for _, body := range []string{
		`{"op":"unknown","id":"receipt","journal":[{"op":"add","text":"must not apply"}]}`,
		`{"op":"list","id":"receipt","journal":[{"op":"add","text":"must not apply"}]}`,
		`{"op":"finish","id":"receipt","journal":[{"op":"add","text":"must not apply"}]}`,
		`{"op":"finish","id":"receipt","text":"invalid","journal":[{"op":"add","text":"must not apply"}]}`,
		`{"complete":true,"journal":[{"op":"add","text":"must not apply"}],"id":"receipt"}`,
	} {
		request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		broker.serveHTTP(response, request)
		if response.Code != http.StatusBadRequest || calls != 0 {
			t.Fatalf("invalid request changed state: status=%d calls=%d", response.Code, calls)
		}
	}
}

func TestShellJournalConcurrentIDsAndPinnedQuestion(t *testing.T) {
	transform, proxy, _, _ := newMekugiTestTransform(t)
	var expectedMu sync.Mutex
	expected := make(map[string]string)
	publish := proxy.commentary.journalPublisher
	proxy.commentary.journalPublisher = func(ctx context.Context, session, thread, receipt string, mutations []journalMutation) ([]string, error) {
		ids, err := publish(ctx, session, thread, receipt, mutations)
		if err == nil && len(mutations) == 1 && mutations[0].Op == "add" {
			expectedMu.Lock()
			expected[*mutations[0].Text] = ids[0]
			expectedMu.Unlock()
		}
		return ids, err
	}
	server := httptest.NewServer(http.HandlerFunc(proxy.commentary.serveHTTP))
	t.Cleanup(server.Close)
	proxy.commentaryEndpoint = server.URL
	transform.journalQuestion = "Original question"
	transform.shellTurnID = "turn"
	contribution, _ := proxy.registry.contribution("shell")
	token := transform.subscribeShellJournal("shell-old", contribution)
	sink := &httpShellCommentarySink{endpoint: server.URL, token: token, client: server.Client()}

	// A new request and publisher may coexist with the old running invocation.
	transform.journalQuestion = "Steered question"
	proxy.prepareShellCommentary(transform.shellThreadID, transform.historySessionID, "")
	transform.subscribeShellJournal("shell-new", contribution)
	const count = 32
	var wg sync.WaitGroup
	for i := range count {
		wg.Go(func() {
			value := fmt.Sprintf("answer-%d", i)
			added, err := sink.RequestJournal(t.Context(), shellJournalCommand{
				Op: "add", Mutation: &journalMutation{Op: "add", Text: new(value), Answer: new(true)},
			})
			if err != nil || len(added.IDs) != 1 {
				t.Errorf("add failed: %+v, %v", added, err)
				return
			}
			expectedMu.Lock()
			want := expected[value]
			expectedMu.Unlock()
			if added.IDs[0] != want {
				t.Errorf("publication %q returned %q, want %q", value, added.IDs[0], want)
			}
			_, err = sink.RequestJournal(t.Context(), shellJournalCommand{
				Op: "edit", Mutation: &journalMutation{Op: "edit", ID: added.IDs[0], Text: new(added.IDs[0] + ":" + value)},
			})
			if err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	items, err := proxy.journals.list(t.Context(), proxy.replayStore, transform.directory, transform.shellThreadID)
	if err != nil || len(items) != count {
		t.Fatalf("items=%d, %v", len(items), err)
	}
	for _, item := range items {
		if !strings.HasPrefix(item.Text, item.ID+":") || item.Question != "Original question" {
			t.Fatalf("crossed publication identity or provenance: %+v", item)
		}
	}
}

func TestShellJournalListCompleteEncodedCapacity(t *testing.T) {
	broker := newCommentaryBroker()
	t.Cleanup(broker.close)
	store := newJournalStore()
	author := strings.Repeat("<", maxJournalItemBytes/2)
	if err := store.initialize(t.Context(), nil, "workspace", "thread", author, ""); err != nil {
		t.Fatal(err)
	}
	mutations := make([]journalMutation, maxJournalItems)
	for i := range mutations {
		mutations[i] = journalMutation{Op: "add", Text: new(strings.Repeat("\x01", maxJournalItemBytes))}
	}
	if _, err := store.apply(t.Context(), nil, "workspace", "thread", "seed", mutations); err != nil {
		t.Fatal(err)
	}
	broker.journalLister = func(ctx context.Context, _, _, _ string) ([]journalItem, error) {
		return store.list(ctx, nil, "workspace", "thread")
	}
	server := httptest.NewServer(http.HandlerFunc(broker.serveHTTP))
	t.Cleanup(server.Close)
	sink := &httpShellCommentarySink{
		endpoint: server.URL, token: broker.subscribeThread("workspace\x00thread", "thread", author), client: server.Client(),
	}
	result, err := sink.RequestJournal(t.Context(), shellJournalCommand{Op: "list"})
	if err != nil || len(result.Items) != maxJournalItems {
		t.Fatalf("full list: count=%d, %v", len(result.Items), err)
	}
	for _, item := range result.Items {
		if len(item.Text) != maxJournalItemBytes || item.Author != author {
			t.Fatal("list content was truncated")
		}
	}
}

func TestShellJournalResponseOverflowIsExplicit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		chunk := bytes.Repeat([]byte("x"), 1<<20)
		for remaining := maxJournalPublicationResponseBytes + 1; remaining > 0; {
			size := min(remaining, len(chunk))
			if _, err := w.Write(chunk[:size]); err != nil {
				return
			}
			remaining -= size
		}
	}))
	t.Cleanup(server.Close)
	sink := &httpShellCommentarySink{endpoint: server.URL, token: "test", client: server.Client()}
	if _, err := sink.RequestJournal(t.Context(), shellJournalCommand{Op: "list"}); err == nil || !strings.Contains(err.Error(), "response limit") {
		t.Fatalf("overflow error = %v", err)
	}
}
