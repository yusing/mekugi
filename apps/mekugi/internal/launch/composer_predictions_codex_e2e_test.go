//go:build codex_e2e

package launch

import (
	"context"
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/backends/codex/appserver"
)

func TestComposerPredictionNativeCodex(t *testing.T) {
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
	defer cancel()
	var mu sync.Mutex
	var requests [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/responses") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		requests = append(requests, body)
		count := len(requests)
		mu.Unlock()
		answer := "Fixture parent answer."
		if count > 1 {
			answer = `{"suggestion":"Show tests"}`
		}
		response := routerFaultCodexSuccessResponse()
		defer response.Body.Close()
		wire, _ := io.ReadAll(response.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, strings.ReplaceAll(string(wire), strconv.Quote("Recovered after a retry."), strconv.Quote(answer)))
	}))
	defer server.Close()
	cmd := exec.CommandContext(ctx, codex, "app-server", "-c", `model_provider="openai"`, "-c", `openai_base_url=`+strconv.Quote(server.URL+"/v1"), "-c", `model="gpt-6-astra"`, "-c", "features.plugins=false", "-c", "include_collaboration_mode_instructions=false")
	cmd.Env = append(routerFaultCodexEnvironment(t), "OPENAI_API_KEY=fixture-token")
	cmd.Dir = t.TempDir()
	client, err := sessioncontrol.Start[struct{}](cmd, func(appserver.Message) {})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Host.Close()
	var queued []appserver.Message
	read := func() appserver.Message {
		t.Helper()
		select {
		case m := <-client.Host.Messages:
			return m
		case <-ctx.Done():
			t.Fatal(ctx.Err())
			return appserver.Message{}
		}
	}
	rpc := func(method string, params any) appserver.Message {
		t.Helper()
		var id string
		var err error
		if method == "initialize" {
			id, err = client.Requests.Initialize(struct{}{})
		} else {
			id, err = client.Requests.Send(method, params, "main", struct{}{})
		}
		if err != nil {
			t.Fatal(err)
		}
		for {
			m := read()
			if string(m.ID) == id {
				client.Requests.Take(id)
				if m.Error != nil {
					t.Fatalf("%s: %+v", method, m.Error)
				}
				return m
			}
			queued = append(queued, m)
		}
	}
	rpc("initialize", nil)
	if err := client.Requests.Notify("initialized", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	m := rpc("thread/start", map[string]any{"cwd": cmd.Dir, "approvalPolicy": "never", "sandbox": "read-only"})
	var started struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(m.Result, &started); err != nil {
		t.Fatal(err)
	}
	main := started.Thread.ID
	m = rpc("turn/start", map[string]any{"threadId": main, "input": appserver.Input("Fixture parent prompt.")})
	var turn struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(m.Result, &turn); err != nil {
		t.Fatal(err)
	}
	next := func() appserver.Message {
		if len(queued) > 0 {
			m := queued[0]
			queued = queued[1:]
			return m
		}
		return read()
	}
	for {
		m := next()
		if m.Method == "turn/completed" {
			break
		}
	}
	p := &sessioncontrol.Prediction{SourceThread: main, SourceTurn: turn.Turn.ID, Trigger: "live", Prompt: "Fixture remote prompt. Return suggestion JSON."}
	if err := client.Requests.StartPrediction(p); err != nil {
		t.Fatal(err)
	}
	observations := new(sessioncontrol.Observations)
	for !p.Completed || client.Requests.Pending() {
		m := next()
		var response sessioncontrol.Request[struct{}]
		if m.Method == "" {
			response, _ = client.Requests.Take(string(m.ID))
		}
		if _, err := client.ObservePrediction(m, response, observations); err != nil {
			t.Fatal(err)
		}
		if p.Closed && !p.Completed {
			t.Fatalf("prediction rejected: %+v, message=%+v", p, m)
		}
	}
	if p.Text != "Show tests" || p.Closed || p.Thread == main || observations.IsSideThread(p.Thread) {
		t.Fatalf("prediction result or cleanup failed: %+v", p)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 || !strings.Contains(string(requests[1]), "Fixture parent prompt.") {
		t.Fatalf("prediction lost parent context or repeated inference: requests=%d", len(requests))
	}
	var prediction struct {
		Tools []any  `json:"tools"`
		Model string `json:"model"`
	}
	if err := json.Unmarshal(requests[1], &prediction); err != nil {
		t.Fatal(err)
	}
	if len(prediction.Tools) != 0 || prediction.Model != "gpt-6-astra" {
		t.Fatal("host prediction mode exposed tools or changed model")
	}
}
