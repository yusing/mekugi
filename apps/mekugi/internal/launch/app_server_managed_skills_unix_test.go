//go:build unix

package launch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

func heldManagedCatalog(t *testing.T, u *appServerUI) (release func(), started func() int) {
	t.Helper()
	testManagedSkills(t, u, `if [ "$1" = list ]; then
printf '%s' "$$" > helper-started
read -r release < helper-release
fi
`+managedCatalogScript)
	gate := filepath.Join(u.attached.cwd, "helper-release")
	if err := syscall.Mkfifo(gate, 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(gate, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return func() {
			if _, err := file.WriteString("release\n"); err != nil {
				t.Fatal(err)
			}
		}, func() int {
			t.Helper()
			deadline := time.After(5 * time.Second)
			tick := time.NewTicker(time.Millisecond)
			defer tick.Stop()
			for {
				data, err := os.ReadFile(filepath.Join(u.attached.cwd, "helper-started"))
				if err == nil {
					if pid, err := strconv.Atoi(string(data)); err == nil {
						return pid
					}
				}
				select {
				case <-deadline:
					t.Fatal("managed helper did not reach its barrier")
				case <-tick.C:
				}
			}
		}
}

func TestManagedSkillAcquisitionDoesNotHoldDispatcher(t *testing.T) {
	u, wire := newAppServerTestUI()
	release, started := heldManagedCatalog(t, u)
	appServerTestKeys(t, u, "$r")
	request := pickerRequests(t, wire)[0]
	pickerReply(t, u, request.ID, `{"data":[]}`)
	started()
	load := u.picker.skillsLoad
	if load == nil || !u.picker.Loading {
		t.Fatal("catalog did not remain pending behind the helper")
	}
	appServerTestKeys(t, u, "eview")
	appServerTestMessage(t, u, `{"method":"turn/started","params":{"threadId":"main","turn":{"id":"independent"}}}`)
	if u.Turn != "independent" || u.picker.skillsLoad != load || len(pickerRequests(t, wire)) != 1 {
		t.Fatal("host event or query coalescing stalled behind the helper")
	}
	u.Editor.Replace(composer.Draft{Text: "@file"})
	u.picker.refreshPicker(&u.attached, u.clock, u.Requests, u.Editor, u.ctx, u.skillEnvironment)
	requests := pickerRequests(t, wire)
	if len(requests) != 2 || requests[1].Method != "fuzzyFileSearch" {
		t.Fatal("file search waited for an independent skill supplement")
	}
	pickerReply(t, u, requests[1].ID, fmt.Sprintf(`{"files":[{"root":%q,"path":"file.go"}]}`, u.attached.cwd))
	if len(u.picker.Choices) != 1 || u.picker.Choices[0].Path != "file.go" {
		t.Fatal("file search did not settle while the skill helper was held")
	}
	u.Editor.Replace(composer.Draft{Text: "$review"})
	u.picker.refreshPicker(&u.attached, u.clock, u.Requests, u.Editor, u.ctx, u.skillEnvironment)
	release()
	awaitManagedCatalog(t, u)
	if len(u.picker.Choices) != 1 || u.picker.Choices[0].Name != "review" || u.picker.Target.Query != "review" || len(pickerRequests(t, wire)) != 2 {
		t.Fatalf("latest query did not adopt the shared catalog: %+v", u.picker.Choices)
	}
}

func TestManagedSkillNativePublicationAdoptsCurrentQuery(t *testing.T) {
	u, wire := newAppServerTestUI()
	release, started := heldManagedCatalog(t, u)
	u.startSession("main", u.attached.cwd)
	h := remoteOwnerView(t, u)
	view := h.view
	editor := &composer.Editor{Text: "$rev"}
	view.picker.refreshPicker(&view.attached, u.clock, u.Requests, editor, view.ctx, u.skillEnvironment)
	request := pickerRequests(t, wire)[0]
	response, ok := u.Requests.Take(strconv.Itoa(request.ID))
	if !ok {
		t.Fatal("view catalog request missing")
	}
	editor.Replace(composer.Draft{Text: "$review"})
	view.picker.refreshPicker(&view.attached, u.clock, u.Requests, editor, view.ctx, u.skillEnvironment)
	if _, err := u.nativeSession.dispatchViewResponse(&view.attached, &view.picker, &editor, u.clock, &view.panel, &view.resume, view.ctx, response, appserver.Message{Result: []byte(`{"data":[]}`)}); err != nil {
		t.Fatal(err)
	}
	started()
	release()
	select {
	case <-view.picker.skillsLoad.done:
	case <-time.After(5 * time.Second):
		t.Fatal("managed helper did not complete")
	}
	h.views.publish(&u.attached, &u.Client, u.settings, u.serviceTiers, "", "", false, u.side, u.review, u.reviewSource, sessionapi.NativeSessionFacts{}, time.Now())
	if view.picker.skillsLoad != nil || len(view.picker.Choices) != 1 || view.picker.Choices[0].Name != "review" || view.picker.Target.Query != "review" || !view.picker.SkillsLoaded {
		t.Fatal("native publication did not adopt completion for the current query")
	}
}

func TestManagedSkillAcquisitionRejectsRetiredIdentity(t *testing.T) {
	for _, change := range []string{"catalog", "workspace", "workspace-return", "management", "context"} {
		t.Run(change, func(t *testing.T) {
			u, wire := newAppServerTestUI()
			_, started := heldManagedCatalog(t, u)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			u.ctx = ctx
			appServerTestKeys(t, u, "$rev")
			request := pickerRequests(t, wire)[0]
			pickerReply(t, u, request.ID, `{"data":[]}`)
			pid, load := started(), u.picker.skillsLoad
			switch change {
			case "catalog":
				appServerTestMessage(t, u, `{"method":"skills/changed"}`)
			case "workspace", "workspace-return":
				previous := u.attached.cwd
				u.attached.cwd = t.TempDir()
				u.picker.refreshPicker(&u.attached, u.clock, u.Requests, u.Editor, u.ctx, u.skillEnvironment)
				if change == "workspace-return" {
					u.attached.cwd = previous
				}
			case "management":
				u.picker.Kind = "manage"
				u.picker.refreshPicker(&u.attached, u.clock, u.Requests, u.Editor, u.ctx, u.skillEnvironment)
				u.picker.Kind = ""
			case "context":
				cancel()
			}
			select {
			case <-load.done:
			case <-time.After(5 * time.Second):
				t.Fatal("retired helper was not canceled and drained")
			}
			u.managedSkills.Wait()
			if !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
				t.Fatal("runtime join returned with the helper still alive")
			}
			if !u.picker.pollManagedSkills(&u.attached, u.clock) || u.picker.SkillsLoaded || len(u.picker.Skills) != 0 || strings.Contains(u.attached.feedback.Text, "skills-mgr:") {
				t.Fatal("retired acquisition published a catalog or failure")
			}
			if change != "context" {
				u.picker.refreshPicker(&u.attached, u.clock, u.Requests, u.Editor, u.ctx, u.skillEnvironment)
				if len(pickerRequests(t, wire)) != 2 {
					t.Fatal("current identity did not start a new acquisition")
				}
			}
		})
	}
}

func TestManagedSkillNativeViewRetiresPhysicalWork(t *testing.T) {
	for _, action := range []string{"replace", "dispose", "shutdown"} {
		t.Run(action, func(t *testing.T) {
			u, wire := newAppServerTestUI()
			_, started := heldManagedCatalog(t, u)
			u.startSession("main", u.attached.cwd)
			n := newNativeViews(t.Context(), sessionapi.NewLive().Native(), u.presentationState, u.activity, u.Requests)
			defer n.close()
			request := sessionapi.NativeRequest{Attachment: "view", Thread: "main", Workspace: u.attached.cwd, Context: t.Context(), ViewContext: t.Context()}
			if reason := n.attach(request, &u.attached, u.Thread); reason != "" {
				t.Fatal(reason)
			}
			view := n.views["view"]
			editor := &composer.Editor{Text: "$rev"}
			view.picker.refreshPicker(&view.attached, u.clock, u.Requests, editor, view.ctx, u.skillEnvironment)
			pending := pickerRequests(t, wire)[0]
			response, ok := u.Requests.Take(strconv.Itoa(pending.ID))
			if !ok {
				t.Fatal("view catalog request missing")
			}
			if _, err := u.nativeSession.dispatchViewResponse(&view.attached, &view.picker, &editor, u.clock, &view.panel, &view.resume, view.ctx, response, appserver.Message{Result: []byte(`{"data":[]}`)}); err != nil {
				t.Fatal(err)
			}
			pid, load := started(), view.picker.skillsLoad
			switch action {
			case "replace":
				n.replacePresentation(view, &u.attached)
			case "dispose":
				n.dispose("view")
			case "shutdown":
				n.close()
			}
			select {
			case <-load.done:
			case <-time.After(5 * time.Second):
				t.Fatal("view left its managed helper running")
			}
			u.managedSkills.Wait()
			if !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) || view.picker.skillsLoad != nil {
				t.Fatal("view retirement did not release physical work and adoption")
			}
		})
	}
}
