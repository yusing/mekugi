package router

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestJournalMCPMutate(t *testing.T) {
	t.Parallel()
	workspace, storage := t.TempDir(), t.TempDir()
	replay, err := openMekugiReplayStore(storage)
	if err != nil {
		t.Fatal(err)
	}
	ctx, release, err := replay.beginSession(t.Context(), "thread", "host-session")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	store := newJournalStore()
	if err := store.initialize(ctx, replay, workspace, "thread", "/root", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.bindIdentity(ctx, replay, workspace, "thread", "", "/root", true); err != nil {
		t.Fatal(err)
	}
	proxy := &mekugiProxy{journals: store, replayStore: replay}
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := newJournalMCPServer(proxy).Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	meta := mcp.Meta{"threadId": "thread", "sessionId": "host-session", "itemId": "item", "callId": "plan", codexTurnMetadataHeader: map[string]any{"thread_id": "thread", "turn_id": "turn"}}
	call := func(arguments any, fail bool) *mcp.CallToolResult {
		t.Helper()
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "journal_mutate", Arguments: arguments, Meta: meta})
		if err != nil || result.IsError != fail {
			t.Fatalf("call = %s, %v, want error %v", mustMarshalJSON(result), err, fail)
		}
		return result
	}
	batch := func(ops ...map[string]any) map[string]any { return map[string]any{"mutations": ops} }
	plan := batch(map[string]any{"op": "plan", "tasks": []any{map[string]any{"title": "Parent", "tasks": []string{"Child"}}}})
	result := call(plan, false)
	data, err := json.Marshal(result.StructuredContent)
	if err != nil || !bytes.Equal(data, []byte(`{"paths":["/1","/1/1"]}`)) {
		t.Fatalf("paths = %s, %v", data, err)
	}
	// Reopen storage to prove receipt idempotence, not only in-memory deduplication.
	proxy.replayStore, err = openMekugiReplayStore(storage)
	if err != nil {
		t.Fatal(err)
	}
	proxy.journals = newJournalStore()
	call(plan, false)
	call(batch(map[string]any{"op": "log", "text": "Changed replay"}), true)
	meta["callId"] = "reject"
	call(batch(map[string]any{"op": "log", "text": "Must roll back"}, map[string]any{"op": "set", "p": "/missing", "state": "done"}), true)
	call(batch(map[string]any{"op": "add", "kind": "note", "title": "Invalid state", "state": "working"}), true)
	call(batch(map[string]any{"op": "plan", "tasks": []any{map[string]any{"title": "Invalid", "tasks": []any{map[string]any{"title": "Nested", "extra": true}}}}}), true)
	call(batch(map[string]any{"op": "log", "text": "Must also roll back"}, map[string]any{"op": "finish"}), true)
	nodes, err := proxy.journals.readTree(ctx, proxy.replayStore, workspace, "thread", "", "", nil, "own")
	if err != nil || len(nodes) != 1 || len(nodes[0].Children) != 1 {
		t.Fatalf("atomic rollback = %+v, %v", nodes, err)
	}
	meta["callId"] = "finish"
	call(batch(map[string]any{"op": "set", "p": "/1/1", "state": "done"}, map[string]any{"op": "set", "p": "/1", "state": "done"}, map[string]any{"op": "finish"}), false)
	var found bool
	err = proxy.journals.transaction(ctx, proxy.replayStore, workspace, "thread", func(j *threadJournal, _ bool) error {
		_, found = j.Receipts["runtime:"+journalHostFinishReceipt("turn", "item")]
		return errJournalUnchanged
	})
	if err != nil || !found {
		t.Fatalf("finish receipt = %v, %v", found, err)
	}
	// Separate nested calls share an enclosing exec item, not mutation identity.
	meta["callId"] = "second finish"
	secondFinish := batch(map[string]any{"op": "log", "text": "Second completed batch"}, map[string]any{"op": "finish"})
	call(secondFinish, false)
	proxy.replayStore, err = openMekugiReplayStore(storage)
	if err != nil {
		t.Fatal(err)
	}
	proxy.journals = newJournalStore()
	call(secondFinish, false)
	call(batch(map[string]any{"op": "log", "text": "Changed second batch"}, map[string]any{"op": "finish"}), true)
	call(batch(map[string]any{"op": "log", "text": "Second completed batch"}), true)
	nodes, err = proxy.journals.readTree(ctx, proxy.replayStore, workspace, "thread", "", "", nil, "own")
	if err != nil || len(nodes) != 2 || nodes[1].Title != "Second completed batch" {
		t.Fatalf("finish replay duplicated or changed mutations: %+v, %v", nodes, err)
	}
	meta["callId"] = "marker replay"
	call(batch(map[string]any{"op": "log", "text": "Marker replay"}), false)
	call(batch(map[string]any{"op": "log", "text": "Marker replay"}, map[string]any{"op": "finish"}), true)
	meta["callId"] = "missing identity"
	delete(meta, "itemId")
	call(batch(map[string]any{"op": "log", "text": "Missing origin"}), true)
}

func TestJournalMCPRead(t *testing.T) {
	t.Parallel()
	replay, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	store := newJournalStore()
	for _, identity := range []struct{ thread, parent, author string }{
		{"root", "", "/root"}, {"child", "root", "/root/child"}, {"other", "root", "/root/other"},
	} {
		ctx, release, err := replay.beginSession(t.Context(), identity.thread, "seed")
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		if err := store.initialize(ctx, replay, workspace, identity.thread, identity.author, ""); err != nil {
			t.Fatal(err)
		}
		if err := store.bindIdentity(ctx, replay, workspace, identity.thread, identity.parent, identity.author, true); err != nil {
			t.Fatal(err)
		}
		if _, err := store.apply(ctx, replay, workspace, identity.thread, "seed", []journalMutation{{Op: "add", Title: new(identity.thread)}}); err != nil {
			t.Fatal(err)
		}
	}
	ctx, release, err := replay.beginSession(t.Context(), "ambiguous", "seed")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for _, directory := range []string{workspace, t.TempDir()} {
		if err := store.initialize(ctx, replay, directory, "ambiguous", "/root", ""); err != nil {
			t.Fatal(err)
		}
		if err := store.bindIdentity(ctx, replay, directory, "ambiguous", "", "/root", true); err != nil {
			t.Fatal(err)
		}
	}
	// A new store must authorize from durable identity, without a live parent.
	proxy := &mekugiProxy{journals: newJournalStore(), replayStore: replay}
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	server := newJournalMCPServer(proxy)
	serverSession, err := server.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	listed, err := client.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(listed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"name":"journal_read"`)) || !bytes.Contains(data, []byte(`"additionalProperties":false`)) {
		t.Fatalf("native schema discovery: %s", data)
	}
	for _, test := range []struct {
		name, thread, agent string
		arguments           map[string]any
		want, failure       string
	}{
		{name: "own", thread: "child", want: "child"},
		{name: "ancestor", thread: "child", agent: "/root", want: "root"},
		{name: "foreign", thread: "child", agent: "/root/other", failure: "isError"},
		{name: "missing identity", failure: "identity"},
		{name: "unknown thread", thread: "unknown", failure: "isError"},
		{name: "ambiguous workspace", thread: "ambiguous", failure: "ambiguous"},
		{name: "unknown field", thread: "child", arguments: map[string]any{"workspace": workspace}, failure: "isError"},
		{name: "invalid view", thread: "child", arguments: map[string]any{"view": "invalid"}, failure: "isError"},
		{name: "invalid depth", thread: "child", arguments: map[string]any{"depth": -1}, failure: "isError"},
	} {
		t.Run(test.name, func(t *testing.T) {
			arguments := test.arguments
			if arguments == nil {
				arguments = map[string]any{"view": "own", "depth": 0}
				if test.agent != "" {
					arguments["agent"] = test.agent
				}
			}
			result, err := client.CallTool(t.Context(), &mcp.CallToolParams{
				Name: "journal_read", Arguments: arguments,
				Meta: mcp.Meta{"threadId": test.thread, "sessionId": "host-session"},
			})
			if err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if test.failure != "" {
				if !result.IsError || !bytes.Contains(data, []byte(test.failure)) || result.StructuredContent != nil {
					t.Fatalf("rejection: %s", data)
				}
				return
			}
			var response struct {
				Nodes []journalNode `json:"nodes"`
			}
			content, err := json.Marshal(result.StructuredContent)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(content, &response); err != nil {
				t.Fatal(err)
			}
			nodes := response.Nodes
			if len(nodes) != 1 || nodes[0].Title != test.want || len(result.Content) == 0 {
				t.Fatalf("read result: %s", data)
			}
		})
	}
	t.Run("cancel blocked read", func(t *testing.T) {
		release, err := proxy.journals.lockState(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		entered, exited := make(chan struct{}), make(chan struct{})
		server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
			return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
				if method == "tools/call" {
					close(entered)
					defer close(exited)
				}
				return next(ctx, method, request)
			}
		})
		deadline, stop := context.WithTimeout(t.Context(), 5*time.Second)
		defer stop()
		ctx, cancel := context.WithCancel(deadline)
		defer cancel()
		done := make(chan error, 1)
		go func() {
			_, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "journal_read", Arguments: map[string]any{"view": "own"}, Meta: mcp.Meta{"threadId": "child", "sessionId": "host-session"}})
			done <- err
		}()
		select {
		case <-entered:
		case <-deadline.Done():
			t.Fatal("read did not reach MCP server")
		}
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled read: %v", err)
		}
		select {
		case <-exited:
		case <-deadline.Done():
			t.Fatal("blocked journal handler did not cancel")
		}
	})

}

func TestJournalMCPServiceStop(t *testing.T) {
	t.Parallel()
	entered, exited := make(chan struct{}), make(chan struct{})
	server := mcp.NewServer(&mcp.Implementation{Name: "blocked", Version: "1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "blocked"}, func(ctx context.Context, _ *mcp.CallToolRequest, _ map[string]any) (*mcp.CallToolResult, any, error) {
		close(entered)
		defer close(exited)
		<-ctx.Done()
		return nil, nil, ctx.Err()
	})
	socket, stop, err := startJournalMCP(t.Context(), server)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Dir(socket))
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("private socket directory: %v %v", info, err)
	}
	connection, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	client, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(t.Context(), &mcp.IOTransport{Reader: connection, Writer: connection}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	result := make(chan error, 1)
	go func() {
		_, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "blocked", Arguments: map[string]any{}})
		result <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not start")
	}
	stopped := make(chan error, 1)
	go func() { stopped <- stop() }()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		connection.Close()
		t.Fatal("service shutdown did not cancel its request")
	}
	<-exited
	if err := <-result; err == nil {
		t.Fatal("blocked request succeeded after shutdown")
	}
	if _, err := os.Stat(filepath.Dir(socket)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket directory remains: %v", err)
	}
}

func TestJournalMCPBridgeCancellation(t *testing.T) {
	t.Parallel()
	socket := filepath.Join(t.TempDir(), "journal.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	input, inputWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer inputWriter.Close()
	outputReader, output, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	defer outputReader.Close()
	congested := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			congested <- err
			return
		}
		defer connection.Close()
		connection.SetWriteDeadline(time.Now().Add(time.Second))
		_, err = connection.Write(make([]byte, 8<<20))
		congested <- err
		// Keep the connection open after congestion until the bridge cancels.
		_, _ = io.Copy(io.Discard, connection)
	}()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- RunJournalMCPBridge(ctx, socket, input, output) }()
	if err := <-congested; err == nil || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("output did not become backpressured: %v", err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("bridge cancellation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("bridge did not cancel with undrained stdout")
	}
}
