package launch

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/modules/conversation/composer"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
	"github.com/yusing/mekugi/providers/openai"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestComposerPredictionRejectsStaleConfiguration(t *testing.T) {
	for _, change := range []string{"typing", "turn", "disable", "switch"} {
		t.Run(change, func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/models") {
					_, _ = w.Write([]byte(`{}`))
					return
				}
				if strings.HasSuffix(r.URL.Path, "/bootstrap") {
					_, _ = w.Write([]byte(`{}`))
					return
				}
				close(started)
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				_, _ = w.Write([]byte(`{"is_enabled":true,"prompt":"remote","prompt_version":"1","prediction_reasoning_effort":""}`))
			}))
			defer server.Close()
			defer close(release)
			provider := openai.New(server.URL, server.Client())
			response, err := provider.Models(t.Context(), http.Header{"Authorization": {"Bearer fixture"}, openai.AccountIDHeader: {"account"}}, "")
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			u, wire := newAppServerTestUI()
			p := composerPredictions{supported: true, provider: provider, results: make(chan predictionConfiguration, 1)}
			p.observe(appserver.Message{Method: "turn/completed", Params: []byte(`{"threadId":"main","turn":{"id":"source","status":"completed"}}`)}, "main")
			if err := p.sync(t.Context(), &u.attached, u.shell, &u.Client, false, true); err != nil {
				t.Fatal(err)
			}
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("configuration not started")
			}
			request := p.request
			switch change {
			case "typing":
				_, _ = u.Editor.Apply(composer.Action{Kind: composer.InsertAction, Text: "new"})
			case "turn":
				p.observe(appserver.Message{Method: "turn/started", Params: []byte(`{"threadId":"main","turn":{"id":"new"}}`)}, "main")
			case "disable":
				u.shell.Main.PredictionsDisabled = true
			case "switch":
				u.Thread = "replacement"
			}
			if err := p.sync(t.Context(), &u.attached, u.shell, &u.Client, false, true); err != nil {
				t.Fatal(err)
			}
			if p.request != nil {
				t.Fatal("stale destination retained")
			}
			if err := p.configured(predictionConfiguration{request, &openai.PredictionSettings{Enabled: true, Prompt: "stale"}}, &u.Client); err != nil {
				t.Fatal(err)
			}
			if wire.Len() != 0 || u.attached.conversation.DraftView().Prediction != "" {
				t.Fatal("stale configuration started host inference or changed draft")
			}
		})
	}
}

func TestComposerPredictionModerationAndResume(t *testing.T) {
	u, _ := newAppServerTestUI()
	p := composerPredictions{supported: true, provider: openai.New("", nil), results: make(chan predictionConfiguration, 1)}
	p.observe(appserver.Message{Result: []byte(`{"thread":{"id":"main","turns":[{"id":"source","status":"completed"}]}}`)}, "main")
	if p.turn != "source" || p.trigger != "catch_up" {
		t.Fatal("resume did not recover source turn")
	}
	p.observe(appserver.Message{Method: "turn/moderationMetadata", Params: []byte(`{"threadId":"main","turnId":"source","metadata":{"generation":{"a":{"outputs":[{"results":[{"labels":["blocked"]}]}]}}}}`)}, "main")
	_, cancel := context.WithCancel(t.Context())
	defer cancel()
	p.request = &predictionRequest{thread: "main", turn: "source", revision: u.Editor.Revision(), cancel: cancel, operation: &sessioncontrol.Prediction{Completed: true, Text: "hidden"}, settings: &openai.PredictionSettings{Policy: openai.PredictionPolicy{AssistantLabels: []string{"blocked"}}}}
	if err := p.sync(t.Context(), &u.attached, u.shell, &u.Client, false, true); err != nil {
		t.Fatal(err)
	}
	if u.attached.conversation.DraftView().Prediction != "" {
		t.Fatal("moderated suggestion published")
	}
	if len(p.metadata) != 0 {
		t.Fatal("moderation evidence was retained after evaluation")
	}
	p.request.settings.Policy = openai.PredictionPolicy{}
	if err := p.sync(t.Context(), &u.attached, u.shell, &u.Client, false, true); err != nil {
		t.Fatal(err)
	}
	if u.attached.conversation.DraftView().Prediction != "" {
		t.Fatal("consumed suppression decision was lost")
	}
}

func TestComposerPredictionAcceptEditAndSubmit(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.attached.conversation.Prediction = conversationmodel.Prediction{Text: "Show tests", Revision: u.Editor.Revision()}
	appServerTestKeys(t, u, "\t")
	if u.Text != "Show tests" || wire.Len() != 0 {
		t.Fatalf("accept submitted or lost editable text: %q %s", u.Text, wire.String())
	}
	appServerTestKeys(t, u, " please\r")
	if !strings.Contains(wire.String(), "Show tests please") || !strings.Contains(wire.String(), "turn/start") {
		t.Fatal("accepted prediction did not use normal submission")
	}
	u, wire = newAppServerTestUI()
	u.attached.conversation.Prediction = conversationmodel.Prediction{Text: "Show tests", Revision: u.Editor.Revision()}
	appServerTestKeys(t, u, "x\x7f")
	if u.attached.conversation.DraftView().Prediction != "" || wire.Len() != 0 {
		t.Fatal("typing and clearing resurrected prediction")
	}
	appServerTestKeys(t, u, "/predictions off\r")
	state := u.shell.Shell.Preferences(u.shell.Main)
	if !state.PredictionsDisabled || u.Text != "" || wire.Len() != 0 {
		t.Fatal("prediction toggle reached host or did not persist")
	}
	u.shell.Main.PredictionsDisabled = false
	u.shell.Shell.RestorePreferences(state, u.shell.Main)
	if !u.shell.Main.PredictionsDisabled {
		t.Fatal("prediction preference did not restore")
	}
	appServerTestKeys(t, u, "/predictions on\r")
	if u.shell.Main.PredictionsDisabled {
		t.Fatal("enable failed")
	}
}

func TestComposerPredictionModerationBoundAndLateArrival(t *testing.T) {
	for _, scenario := range []string{"overflow", "oversized", "late", "resumed-running"} {
		t.Run(scenario, func(t *testing.T) {
			u, _ := newAppServerTestUI()
			p := composerPredictions{supported: true, provider: openai.New("", nil), results: make(chan predictionConfiguration, 1)}
			if scenario == "resumed-running" {
				p.observe(appserver.Message{Result: []byte(`{"thread":{"id":"main","turns":[{"id":"source","status":"inProgress"}]}}`)}, "main")
			} else {
				p.observe(appserver.Message{Method: "turn/started", Params: []byte(`{"threadId":"main","turn":{"id":"source"}}`)}, "main")
			}
			moderate := func(label, padding string) {
				p.observe(appserver.Message{Method: "turn/moderationMetadata", Params: []byte(fmt.Sprintf(`{"threadId":"main","turnId":"source","metadata":{"padding":%q,"generation":{"a":{"outputs":[{"results":[{"labels":[%q]}]}]}}}}`, padding, label))}, "main")
			}
			switch scenario {
			case "overflow":
				for range 64 {
					moderate("safe", "")
				}
				moderate("blocked", "")
			case "oversized":
				moderate("blocked", strings.Repeat("x", 64<<10))
			case "resumed-running":
				moderate("blocked", "")
			}
			p.observe(appserver.Message{Method: "turn/completed", Params: []byte(`{"threadId":"main","turn":{"id":"source","status":"completed"}}`)}, "main")
			_, cancel := context.WithCancel(t.Context())
			defer cancel()
			p.request = &predictionRequest{thread: "main", turn: "source", revision: u.Editor.Revision(), cancel: cancel, operation: &sessioncontrol.Prediction{Completed: true, Text: "suggestion"}, settings: &openai.PredictionSettings{Policy: openai.PredictionPolicy{AssistantLabels: []string{"blocked"}}}}
			if err := p.sync(t.Context(), &u.attached, u.shell, &u.Client, false, true); err != nil {
				t.Fatal(err)
			}
			if scenario == "late" {
				if u.attached.conversation.DraftView().Prediction != "suggestion" {
					t.Fatal("eligible result not published")
				}
				moderate("blocked", "")
				if err := p.sync(t.Context(), &u.attached, u.shell, &u.Client, false, true); err != nil {
					t.Fatal(err)
				}
			}
			if u.attached.conversation.DraftView().Prediction != "" {
				t.Fatal("moderation failed to suppress source")
			}
		})
	}
}

func TestComposerPredictionLateUnsubscribedTrafficStaysRetired(t *testing.T) {
	u, wire := newAppServerTestUI()
	startAttachedSession(&u.attached, u.observations, "main", t.TempDir())
	p := &sessioncontrol.Prediction{SourceThread: "main", SourceTurn: "source", Prompt: "remote"}
	if err := u.Requests.StartPrediction(p); err != nil {
		t.Fatal(err)
	}
	for _, message := range []string{
		`{"id":1,"result":{"thread":{"id":"main","turns":[{"id":"source","status":"completed"}]},"modelProvider":"openai","model":"gpt-6-astra"}}`,
		`{"id":2,"result":{"thread":{"id":"prediction"}}}`,
		`{"id":3,"result":{"turn":{"id":"turn"}}}`,
		`{"method":"item/completed","params":{"threadId":"prediction","turnId":"turn","item":{"type":"agentMessage","text":"{\"suggestion\":\"Show tests\"}"}}}`,
		`{"method":"turn/completed","params":{"threadId":"prediction","turn":{"id":"turn","status":"completed"}}}`,
		`{"id":4,"result":{}}`,
	} {
		appServerTestMessage(t, u, message)
	}
	if !p.Completed || u.Requests.Pending() {
		t.Fatal("prediction fixture did not settle")
	}
	wire.Reset()
	appServerTestMessage(t, u, `{"method":"item/completed","params":{"threadId":"prediction","turnId":"turn","item":{"id":"late","type":"agentMessage","text":"Auxiliary content"}}}`)
	if wire.Len() != 0 {
		t.Fatal("retired prediction reached Main", wire.String())
	}
}

func TestUISnapshotComposerPrediction(t *testing.T) {
	for _, theme := range []diffrender.Theme{diffrender.TerminalTheme, diffrender.DarkTheme, diffrender.LightTheme} {
		for _, width := range []int{36, 80} {
			t.Run(fmt.Sprintf("%d/%d", theme, width), func(t *testing.T) {
				u, _ := newAppServerTestUI()
				u.shell.Transcript.Painter.Theme = theme
				u.attached.presentation.Status.Text = "Ready"
				u.attached.conversation.Prediction = conversationmodel.Prediction{Text: "Show the tests for this change.", Revision: u.Editor.Revision()}
				rows, _ := u.testMainFrame(width, 8, 0)
				rows = append(rows, "plain after prediction")
				uisnapshot.AssertTerminal(t, fmt.Sprintf("testdata/snapshots/composer-prediction-%d-%d.txt", theme, width), rows, width)
			})
		}
	}
}
