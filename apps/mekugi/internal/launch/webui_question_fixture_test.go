package launch

import (
	"context"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/webui"
	"github.com/yusing/mekugi/modules/conversation/prompt"
)

func TestWebUIQuestionPreviewFixture(t *testing.T) {
	path := os.Getenv("MEKUGI_TEST_WEBUI_QUESTION_FIXTURE")
	if path == "" {
		t.Skip("set MEKUGI_TEST_WEBUI_QUESTION_FIXTURE to a local fixture manifest path after make webui")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Minute)
	defer cancel()
	page := httptest.NewServer(webui.Handler())
	defer page.Close()
	thread := sessionpresentation.Thread{ID: "same-thread", Caller: "/root"}
	frame := sessionapi.NativeFrame{View: sessionpresentation.View{Version: sessionpresentation.Version{Attachment: 1, Revision: 1}, Main: thread, Input: thread, Displayed: thread, Info: sessionpresentation.MainInfo{Workspace: "/workspace"}}, Questions: []prompt.CallView{{ID: "release", Thread: thread.ID, Questions: []prompt.QuestionView{
		{ID: "q1", Header: "Release strategy", Text: "How should we publish the release?", Pending: true, Choices: []prompt.Option{
			{Label: "Immediate", Description: "Publish all changes now", Preview: "Immediate release\n\nPublish the current build to all customers."},
			{Label: "Staged", Description: "Monitor a canary before expanding", Recommended: true, Preview: "Release plan\n\n1. Publish to the canary group.\n2. Monitor errors for one hour.\n3. Expand to all customers.\n\n<b>Plain text, not HTML.</b>\n\n" + strings.Repeat("Check the error budget before expanding.\n", 18)},
			{Label: "Later", Description: "Keep the current version"},
		}},
		{ID: "q2", Header: "Release audience", Text: "Who should receive the announcement?", Pending: true, Choices: []prompt.Option{{Label: "Internal", Preview: "Notify the engineering team."}, {Label: "Customers", Recommended: true, Preview: "Share the release notes with customers."}}},
	}}}}
	var mu sync.Mutex
	var answers []sessionapi.Answer
	const token = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	var machine *webUIBrowserMachine
	machine = newWebUIBrowserMachineWithNative(t, ctx, page.URL, "Question preview", token, func(native *sessionapi.Native, request sessionapi.NativeRequest) bool {
		if request.Attach {
			current := frame
			current.View.Info.Workspace = machine.workdir
			if err := native.Publish(request.Attachment, current); err != nil {
				request.Reply <- err.Error()
			} else {
				request.Reply <- ""
			}
			return true
		}
		if request.Operation.Answers != nil {
			mu.Lock()
			if answer := request.Operation.Answers.PublicAnswer(); answer != nil {
				answers = append(answers, *answer)
			}
			mu.Unlock()
			request.Reply <- ""
			return true
		}
		return false
	})
	done := make(chan struct{})
	var once sync.Once
	controls := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/done" {
			once.Do(func() { close(done) })
			return
		}
		mu.Lock()
		defer mu.Unlock()
		data, err := json.Marshal(answers)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(data)
	}))
	defer controls.Close()
	manifest, err := json.Marshal(map[string]string{"page": page.URL, "endpoint": machine.remote.URL, "token": token, "controls": controls.URL})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, manifest, 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("Question preview fixture ready: %s", path)
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("question preview fixture timed out")
	}
}
