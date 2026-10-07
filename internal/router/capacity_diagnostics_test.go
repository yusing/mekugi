package router

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
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
	token := broker.subscribe("/w\x00thread", "call")
	sink := &httpCommentarySink{endpoint: server.URL, token: token, client: server.Client()}
	for _, publication := range []map[string]any{
		{"id": "fixture-add", "journal": []journalMutation{{Op: "add", Text: new("milestone")}}},
		{"op": "list"},
	} {
		result, err := sink.send(t.Context(), publication)
		if publication["op"] == nil {
			// Mutation rejections are structured results the helper reports.
			if err != nil || !strings.Contains(string(result), `"ok":false`) || !strings.Contains(string(result), "delete obsolete items") {
				t.Fatalf("underlying publisher error lost: %s, %v", result, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), "HTTP 400") || !strings.Contains(err.Error(), "retry initialization") {
			t.Fatalf("underlying lister error lost: %v", err)
		}
	}
}

func TestCapacityNoticesAreVisibleAndDoNotConsumeTools(t *testing.T) {
	issues := NewCriticalErrors()
	broker := newCommentaryBroker()
	broker.notice = func(category, message string) { issues.addNotice("", category, message) }
	for range maxCommentaryRoutes {
		if broker.subscribe("session", "call") == "" {
			t.Fatal("publisher rejected before concurrency limit")
		}
	}
	if broker.subscribe("session", "overflow") != "" {
		t.Fatal("publisher accepted beyond concurrency limit")
	}
	visible := issues.takeNative("any-root", nil)
	if len(visible.notices) != 1 {
		t.Fatal("capacity notice is not available to the root")
	}
	message := noticeText(visible.notices[0])
	if !strings.Contains(message, "256 concurrent publisher routes") || strings.Contains(message, "functions.journal") {
		t.Fatalf("capacity diagnostic: %s", message)
	}
}

func TestJournalCapacityReturnsActionableHostToolError(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is required to execute the lowered exec fixture")
	}
	transform, proxy := newRuntimeCommentaryTransform(t)
	for range maxCommentaryRoutes {
		if proxy.commentary.subscribe("other-session", "call") == "" {
			t.Fatal("publisher capacity exhausted early")
		}
	}
	source := `await tools.exec_command({cmd:"before"});
try { await journal((events.push("argument"), {op:"add",text:"milestone"})); }
catch (error) { events.push(error.message); }
await tools.exec_command({cmd:"after"});`
	lowered, changed, err := transform.lowerCodeModeCommentary("capacity-call", source)
	if err != nil || !changed {
		t.Fatalf("capacity became a translation fault: %v, changed=%v", err, changed)
	}
	script := `const events=[]; const tools={exec_command:async args=>{events.push(args.cmd);return {exit_code:0,output:""};}};
(async()=>{` + lowered + `;process.stdout.write(JSON.stringify(events));})().catch(error=>{console.error(error);process.exitCode=1;});`
	output, err := exec.CommandContext(t.Context(), node, "-e", script).CombinedOutput()
	want := `["before","argument","journal publisher unavailable; finish outstanding calls, then retry await journal(...)","after"]`
	if err != nil || string(output) != want {
		t.Fatalf("lowered host execution: %s, %v; want %s", output, err, want)
	}
	if len(transform.commentarySubscriptions) != 0 {
		t.Fatal("capacity failure retained a publisher subscription")
	}
}

func TestReadCapacityErrorIncludesLimitAndRemedy(t *testing.T) {
	err := validateReadRecord(shellOutputRecord{Stdout: strings.Repeat("x", maxShellOutputBytes+1)})
	if err == nil || !strings.Contains(err.Error(), "limit is "+strconv.Itoa(maxShellOutputBytes)+" bytes") || !strings.Contains(err.Error(), "split the operation") {
		t.Fatalf("missing capacity diagnostic: %v", err)
	}
}
