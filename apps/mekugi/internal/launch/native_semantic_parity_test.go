package launch

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/libraries/commandargs"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
	"github.com/yusing/mekugi/providers/openai"
)

func TestNativePredictionProducerEligibility(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/config") {
			_, _ = io.WriteString(w, `{"is_enabled":true,"prompt":"suggest next input","sample_rate":1}`)
		} else {
			_, _ = io.WriteString(w, `{}`)
		}
	}))
	defer server.Close()
	provider := openai.New(server.URL, server.Client())
	response, err := provider.Models(t.Context(), http.Header{"Authorization": {"Bearer fixture"}, openai.AccountIDHeader: {"account"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	u, wire := newAppServerTestUI()
	u.startSession("main", "/workspace")
	n := newNativeViews(t.Context(), sessionapi.NewLive().Native(), u.presentationState, u.terminalViews, u.activity, u.Requests)
	defer n.close()
	viewContext, disconnect := context.WithCancel(t.Context())
	defer disconnect()
	if refusal := n.attach(sessionapi.NativeRequest{Attachment: "view", Thread: "main", Workspace: "/workspace", Context: t.Context(), ViewContext: viewContext}, &u.attached, "main"); refusal != "" {
		t.Fatal(refusal)
	}
	v := n.views["view"]
	view := publishSessionView(&v.attached, &u.Client, &v.picker, "", preferences.Applied{}, nil, "", "", false, nil, true)
	terminal, _ := u.terminalViews.NewAttachment(u.shell.Transcript.Painter.Theme)
	defer terminal.Diff.Close()
	c := &nativeClient{renderer: nativeClientRenderer{terminal: terminal, views: u.terminalViews}}
	c.notifications.focused = true
	c.accept(sessionapi.NativeEvent{Attachment: "view", Frame: sessionapi.NativeFrame{View: view}})
	if len(c.calls) != 1 || c.calls[0].operation.Focused == nil || !*c.calls[0].operation.Focused {
		t.Fatal("initial attachment omitted terminal focus")
	}
	c.calls = nil
	complete := func() {
		t.Helper()
		c.acquireCompletion()
		if len(c.calls) != 1 || c.calls[0].operation.Completion == nil {
			t.Fatalf("client edit omitted completion metadata: %+v", c.calls)
		}
		v.complete(*c.calls[0].operation.Completion, nil, u.Requests, nil)
		c.calls = nil
		if u.Text != "" || v.attached.presentation.View().Draft.Draft.Text != "" {
			t.Fatal("completion retained a copy of the client draft")
		}
	}
	complete()
	setFocus := func(focused bool) {
		t.Helper()
		input := "\x1b[O"
		if focused {
			input = "\x1b[I"
		}
		c.dispatch(terminalsession.Input{Kind: terminalsession.FocusInput, Text: input})
		if len(c.calls) != 1 || c.calls[0].operation.Focused == nil || *c.calls[0].operation.Focused != focused {
			t.Fatalf("focus was not bound: %+v", c.calls)
		}
		if refusal := v.applyView(c.calls[0].operation); refusal != "" {
			t.Fatal(refusal)
		}
		c.calls = nil
	}
	p := composerPredictions{supported: true, provider: provider, results: make(chan predictionConfiguration, 1)}
	defer p.clear(&u.Client, u.attached.conversation)
	p.observe(appserver.Message{Method: "turn/completed", Params: []byte(`{"threadId":"main","turn":{"id":"source","status":"completed"}}`)}, "main")
	sync := func() {
		t.Helper()
		focused, disabled, available := n.predictionEligibility(&u.attached)
		if err := p.sync(t.Context(), &u.attached, &u.Client, !available, focused, disabled); err != nil {
			t.Fatal(err)
		}
	}
	assertIdle := func() {
		t.Helper()
		sync()
		if p.request != nil || wire.Len() != 0 {
			t.Fatal("ineligible view initiated prediction acquisition")
		}
	}
	assertIdle()
	setFocus(true)
	v.shell.Main.PredictionsDisabled = true
	assertIdle()
	v.shell.Main.PredictionsDisabled = false
	c.editor("main").InsertText("private draft")
	complete()
	assertIdle()
	c.editor("main").Take()
	complete()
	sync()
	var configuration predictionConfiguration
	select {
	case configuration = <-p.results:
	case <-time.After(5 * time.Second):
		t.Fatal("eligible view did not acquire prediction configuration")
	}
	setFocus(false)
	sync()
	if p.request == nil {
		t.Fatal("focus loss canceled an existing prediction request")
	}
	if err := p.configured(configuration, &u.Client); err != nil {
		t.Fatal(err)
	}
	request := btwTestRequest(t, wire, "thread/resume", "main")
	if request.ID == 0 || p.request.operation == nil {
		t.Fatal("prediction did not use the existing host ledger")
	}
	sync()
	if wire.Len() != 0 {
		t.Fatal("another eligibility update initiated a second predictor")
	}
	c.editor("main").InsertText("dismiss prediction")
	complete()
	sync()
	if p.request != nil {
		t.Fatal("editing retained an existing prediction")
	}
	c.editor("main").Take()
	complete()
	setFocus(true)
	sync()
	if p.request != nil {
		t.Fatal("clearing the draft resurrected its dismissed prediction")
	}
	disconnect()
	sync()
	if p.request != nil {
		t.Fatal("disconnected view retained prediction acquisition")
	}
}

func TestNativeBTWControlSocket(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_NATIVE_PARITY_HOST") == "1" {
		serveNativeParityHost(t)
		os.Exit(0)
	}
	for _, recreate := range []bool{false, true} {
		t.Run(fmt.Sprintf("recreate=%t", recreate), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			workspace := installAPIRuntimeHost(t)
			logPath := filepath.Join(t.TempDir(), "host.jsonl")
			t.Setenv("MEKUGI_TEST_NATIVE_PARITY_LOG", logPath)
			bin := t.TempDir()
			script := "#!/bin/sh\nif [ \"$1\" = debug ]; then exit 1; fi\nMEKUGI_TEST_NATIVE_PARITY_HOST=1; export MEKUGI_TEST_NATIVE_PARITY_HOST\nexec " + commandargs.Quote(os.Args[0]) + " -test.run=^TestNativeBTWControlSocket$ -- \"$@\"\n"
			if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			hub := sessionapi.NewHub(t.Context(), &serverLauncher{})
			defer hub.Close()
			dir, err := os.MkdirTemp("", "mekugi-parity-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			socket := filepath.Join(dir, "s")
			listener, err := sessionapi.ListenControl(socket)
			if err != nil {
				t.Fatal(err)
			}
			server := &http.Server{Handler: sessionapi.Versioned(hub.Handler())}
			served := make(chan error, 1)
			go func() { served <- server.Serve(listener) }()
			defer func() { server.Close(); <-served }()
			transport := sessionapi.ControlTransport(socket)
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
			connection, err := sessionapi.StartNative(ctx, client, "http://mekugi", sessionapi.NativeStart{Workspace: workspace, RouterArgs: []string{"--shell-guard=false"}, Args: []string{"--yolo"}})
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			u, _ := newAppServerTestUI()
			terminal, _ := u.terminalViews.NewAttachment(u.shell.Transcript.Painter.Theme)
			defer terminal.Diff.Close()
			c := &nativeClient{renderer: nativeClientRenderer{terminal: terminal, views: u.terminalViews}}
			var event sessionapi.NativeEvent
			await := func(ready func() bool) {
				t.Helper()
				for !ready() {
					event, err = connection.Next()
					if err != nil {
						t.Fatal(err)
					}
					c.accept(event)
					pending := c.calls[:0]
					for _, call := range c.calls {
						if call.operation.Receipt == nil {
							pending = append(pending, call)
							continue
						}
						if _, err := connection.Send(ctx, call.target, call.operation); err != nil {
							t.Fatal(err)
						}
					}
					c.calls = pending
				}
			}
			send := func(operation sessionapi.NativeOperation) {
				t.Helper()
				if _, err := connection.Send(t.Context(), event.Target(), operation); err != nil {
					t.Fatal(err)
				}
			}
			sendLocal := func() {
				t.Helper()
				if len(c.calls) != 1 {
					t.Fatalf("expected one client operation: %+v", c.calls)
				}
				call := c.calls[0]
				c.calls = nil
				send(call.operation)
				if call.after != nil {
					call.after(nil)
				}
			}
			submit := func(text string) {
				t.Helper()
				c.dispatch(terminalsession.Input{Kind: terminalsession.PasteStart})
				c.dispatch(terminalsession.Input{Kind: terminalsession.PasteInput, Text: text})
				c.dispatch(terminalsession.Input{Kind: terminalsession.PasteEnd})
				c.dispatch(terminalsession.Input{Kind: terminalsession.KeyInput, Text: "\r"})
				sendLocal()
			}
			await(func() bool { return event.Frame.View.Main.ID == "main" })
			submit("/side")
			await(func() bool { return event.Frame.View.SideThread.ID == "fork-1" })
			if c.editor("main").Text != "" || c.feedback.Text != "" {
				t.Fatalf("side command remained in Main after selection changed: draft=%q notice=%q", c.editor("main").Text, c.feedback.Text)
			}
			side := event.Frame.View.SideThread
			main := c.editor("main")
			main.InsertText("parked Main draft")
			beforeCopy := c.effect
			submit("/copy")
			await(func() bool { return c.effect > beforeCopy })
			if main.Text != "parked Main draft" || c.editor("side:"+side.ID).Text != "" {
				t.Fatalf("Side command cleanup crossed editors: main=%q side=%q", main.Text, c.editor("side:"+side.ID).Text)
			}
			main.Take()
			send(sessionapi.NativeOperation{Display: &sessionpresentation.DisplayIntent{Version: event.Frame.View.Version, Thread: event.Frame.View.Main}})
			await(func() bool { return event.Frame.View.Displayed == event.Frame.View.Main })
			submit("/btw question")
			await(func() bool {
				return event.Frame.View.BTW != nil && len(event.Frame.View.BTW.Answer) > 0 && !event.Frame.View.BTWRunning
			})
			observer, err := sessionapi.OpenNative(ctx, client, "http://mekugi", "main")
			if err != nil {
				t.Fatal(err)
			}
			defer observer.Close()
			observed, err := observer.Next()
			if err != nil {
				t.Fatal(err)
			}
			// Refresh only this view, then mutate through another view inside the
			// publication interval. Admission must check the execution owner as well.
			fresh, err := connection.Send(t.Context(), event.Target(), sessionapi.NativeOperation{Focused: new(false)})
			if err != nil {
				t.Fatal(err)
			}
			staleTarget := event.Target()
			staleTarget.Version = fresh.Version
			staleExchange := sessionpresentation.CloseSideIntent{Version: fresh.Version, Key: event.Frame.View.BTW.Key, Exchange: event.Frame.View.BTW.ID}
			if recreate {
				close := sessionpresentation.CloseSideIntent{Version: observed.Frame.View.Version, Key: observed.Frame.View.BTW.Key, Exchange: observed.Frame.View.BTW.ID}
				if _, err := observer.Send(t.Context(), observed.Target(), sessionapi.NativeOperation{BTW: &close}); err != nil {
					t.Fatal(err)
				}
				for observed.Frame.View.BTW != nil {
					observed, err = observer.Next()
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := observer.Send(t.Context(), observed.Target(), sessionapi.NativeOperation{Command: new("/btw replacement")}); err != nil {
				t.Fatal(err)
			}
			for observed.Frame.View.BTW == nil || observed.Frame.View.BTW.Question != "replacement" {
				observed, err = observer.Next()
				if err != nil {
					t.Fatal(err)
				}
			}
			await(func() bool {
				return event.Frame.View.BTW != nil && event.Frame.View.BTW.Question == "replacement" && !event.Frame.View.BTWRunning
			})
			if _, err := connection.Send(t.Context(), staleTarget, sessionapi.NativeOperation{BTW: &staleExchange}); err == nil {
				t.Fatal("stale BTW dismissal closed another client's exchange")
			}
			cancelIntent := sessionpresentation.ComposerIntent{Version: staleTarget.Version, Thread: "main", Operation: sessionpresentation.CancelComposer}
			if _, err := connection.Send(t.Context(), staleTarget, sessionapi.NativeOperation{Composer: &sessionapi.NativeComposerInput{Intent: cancelIntent}}); err == nil {
				t.Fatal("stale composer cancellation was allowed to close another client's exchange")
			}
			c.dispatch(terminalsession.Input{Kind: terminalsession.KeyInput, Text: "\x1b"})
			if len(c.calls) != 1 || c.calls[0].operation.BTW == nil || c.calls[0].operation.Side != nil {
				t.Fatalf("Escape did not bind the BTW dock: %+v", c.calls)
			}
			stale := *c.calls[0].operation.BTW
			stale.Exchange++
			if _, err := connection.Send(t.Context(), event.Target(), sessionapi.NativeOperation{BTW: &stale}); err == nil {
				t.Fatal("a different BTW exchange was allowed to close the dock")
			}
			target := event.Target()
			send(sessionapi.NativeOperation{Command: new("/title Renamed Main")})
			await(func() bool {
				return event.Frame.View.Info.Title == "Renamed Main" && event.Frame.View.Version.Revision > target.Version.Revision
			})
			if _, err := connection.Send(t.Context(), target, c.calls[0].operation); err != nil {
				t.Fatalf("title progress prevented closing the same BTW exchange: %v", err)
			}
			c.calls = nil
			await(func() bool { return event.Frame.View.BTW == nil })
			if event.Frame.View.SideThread != side {
				t.Fatal("BTW dismissal closed the editable side conversation")
			}
			serverCLIAwait(t, func() bool {
				body, _ := os.ReadFile(logPath)
				return strings.Contains(string(body), `thread/unsubscribe {"threadId":"fork-2"}`)
			})
			body, _ := os.ReadFile(logPath)
			if strings.Contains(string(body), `thread/unsubscribe {"threadId":"fork-1"}`) {
				t.Fatal("BTW dismissal released the other side owner")
			}
			closeSide := sessionpresentation.CloseSideIntent{Version: event.Frame.View.Version, Key: event.Frame.View.Side.Key, Exchange: event.Frame.View.Side.ID}
			target = event.Target()
			send(sessionapi.NativeOperation{Command: new("/title Main progressed again")})
			await(func() bool {
				return event.Frame.View.Info.Title == "Main progressed again" && event.Frame.View.Version.Revision > target.Version.Revision
			})
			if _, err := connection.Send(t.Context(), target, sessionapi.NativeOperation{Side: &closeSide}); err != nil {
				t.Fatalf("Main progress prevented closing Side: %v", err)
			}
			await(func() bool { return event.Frame.View.Side == nil })
			submit("/side")
			await(func() bool { return event.Frame.View.Side != nil && event.Frame.View.Side.Key != closeSide.Key })
			if _, err := connection.Send(t.Context(), target, sessionapi.NativeOperation{Side: &closeSide}); err == nil {
				t.Fatal("stale Side dismissal closed a replacement owner")
			}
			c.dispatch(terminalsession.Input{Kind: terminalsession.KeyInput, Text: "\x1b"})
			sendLocal()
			await(func() bool { return event.Frame.View.Side == nil })
			submit("/btw after Escape")
			await(func() bool {
				return event.Frame.View.BTW != nil && event.Frame.View.BTW.Question == "after Escape" && !event.Frame.View.BTWRunning
			})
			if c.editor("main").Text != "" {
				t.Fatalf("side cleanup polluted the next BTW command: %q", c.editor("main").Text)
			}
		})
	}
}

func serveNativeParityHost(t *testing.T) {
	t.Helper()
	log, err := os.OpenFile(os.Getenv("MEKUGI_TEST_NATIVE_PARITY_LOG"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	decoder, encoder := jsontext.NewDecoder(os.Stdin), jsontext.NewEncoder(os.Stdout)
	write := func(value any) {
		t.Helper()
		if err := json.MarshalEncode(encoder, value); err != nil {
			t.Fatal(err)
		}
	}
	forks := 0
	for {
		var request struct {
			ID     jsontext.Value `json:"id"`
			Method string         `json:"method"`
			Params jsontext.Value `json:"params"`
		}
		if err := json.UnmarshalDecode(decoder, &request); err != nil {
			if !errors.Is(err, io.EOF) {
				t.Fatal(err)
			}
			return
		}
		if len(request.ID) == 0 {
			continue
		}
		fmt.Fprintf(log, "%s %s\n", request.Method, request.Params)
		result := map[string]any{}
		switch request.Method {
		case "config/read":
			result["layers"] = []any{}
		case "thread/start":
			cwd, _ := os.Getwd()
			result["thread"] = map[string]any{"id": "main", "cwd": cwd}
		case "thread/fork":
			forks++
			result["thread"] = map[string]any{"id": fmt.Sprintf("fork-%d", forks)}
		case "turn/start":
			result["turn"] = map[string]any{"id": "answer-turn"}
		}
		write(map[string]any{"id": request.ID, "result": result})
		if request.Method == "turn/start" {
			var params struct {
				Thread string `json:"threadId"`
			}
			if err := json.Unmarshal(request.Params, &params); err != nil {
				t.Fatal(err)
			}
			write(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": params.Thread, "turnId": "answer-turn", "item": map[string]any{"id": "answer", "type": "agentMessage", "text": "BTW answer"}}})
			write(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": params.Thread, "turn": map[string]any{"id": "answer-turn", "status": "completed"}}})
		}
	}
}
