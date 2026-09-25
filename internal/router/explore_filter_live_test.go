package router

import (
	"context"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

type liveExploreJudge struct {
	client *typesafeClient
	mu     sync.Mutex
	calls  int
	usage  typesafeUsage
}

func (j *liveExploreJudge) nouls(ctx context.Context, state any, questions map[string]typesafeNoul) (map[string]float64, typesafeUsage, error) {
	answers, usage, err := j.client.nouls(ctx, state, questions)
	j.mu.Lock()
	defer j.mu.Unlock()
	j.calls++
	j.usage.InputTokens += usage.InputTokens
	j.usage.OutputTokens += usage.OutputTokens
	return answers, usage, err
}

// Opt in explicitly; routine tests never contact a provider or read .env.
func TestExploreFilterLiveCodeMode(t *testing.T) {
	if os.Getenv("MEKUGI_EXPLORE_LIVE_TEST") != "1" {
		t.Skip("set MEKUGI_EXPLORE_LIVE_TEST=1 and TYPESAFE_API_KEY for live verification")
	}
	key := os.Getenv("TYPESAFE_API_KEY")
	if key == "" {
		t.Fatal("TYPESAFE_API_KEY is required")
	}
	dir, body := exploreFixture(t)
	storeDir := t.TempDir()
	store, err := openMekugiReplayStore(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	result, err := json.Marshal(map[string]any{"exit_code": 0, "output": body, "wall_time_seconds": 0.1, "chunk_id": "live-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	request := exploreRequest(t, dir, "rg -n snapshot", "")
	var items []map[string]jsonv1.RawMessage
	if err := json.Unmarshal(request.fields["input"], &items); err != nil {
		t.Fatal(err)
	}
	arguments := jsonString(items[2], "arguments")
	items[2] = map[string]jsonv1.RawMessage{
		"type": mustMarshalJSON("custom_tool_call"), "name": mustMarshalJSON("exec"), "call_id": mustMarshalJSON("live-code-mode"),
		"input": mustMarshalJSON("text(await tools.exec_command(" + arguments + "));"),
	}
	items[3] = map[string]jsonv1.RawMessage{
		"type": mustMarshalJSON("custom_tool_call_output"), "call_id": mustMarshalJSON("live-code-mode"),
		"output": mustMarshalJSON([]map[string]string{
			{"type": "input_text", "text": "Script completed\nWall time 0.1 seconds\nOutput:\n"},
			{"type": "input_text", "text": string(result)},
		}),
	}
	original := mustMarshalJSON(items)
	request.setInput(original)
	judge := &liveExploreJudge{client: newTypesafeClient(key)}
	filter := newExploreFilter(judge)
	started := time.Now()
	filter.project(t.Context(), request, nil, dir, "", "/root", store)
	elapsed := time.Since(started)
	if err := json.Unmarshal(request.fields["input"], &items); err != nil {
		t.Fatal(err)
	}
	texts := executionOutputTexts(items[3]["output"])
	if len(texts) != 2 {
		t.Fatal("lost Code Mode envelope")
	}
	var projected struct {
		Output string `json:"output"`
	}
	if err := json.Unmarshal([]byte(texts[1]), &projected); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"live/diff.go:1:", "live/view.go:8:"} {
		if !strings.Contains(projected.Output, name) {
			t.Fatalf("live model removed required row %s", name)
		}
	}
	ref := regexp.MustCompile(`Full output: mread ([a-z]+[0-9]*)\]`).FindStringSubmatch(projected.Output)
	if len(ref) != 2 {
		t.Fatal("live fixture did not filter; inspect model eligibility or scores")
	}
	reopened, err := openMekugiReplayStore(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	retained, err := reopened.readShellOutput(t.Context(), ref[1])
	if err != nil || retained.Stdout != body {
		t.Fatal("full original output was not durably recoverable")
	}
	filtered := string(request.fields["input"])
	calls := judge.calls
	request.setInput(original)
	filter.project(t.Context(), request, nil, dir, "", "/root", store)
	if string(request.fields["input"]) != filtered || judge.calls != calls {
		t.Fatal("replay changed output or rejudged")
	}
	t.Logf("live Code Mode fixture: stdout %d -> %d bytes, saved %.1f%%; latency=%s; requests=%d; provider tokens input=%d output=%d; required rows, durable recovery and replay verified", len(body), len(projected.Output), 100*float64(len(body)-len(projected.Output))/float64(len(body)), elapsed.Round(time.Millisecond), calls, judge.usage.InputTokens, judge.usage.OutputTokens)
}
