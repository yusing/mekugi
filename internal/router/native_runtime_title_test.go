package router

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/session"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

type runtimeTitleTestClient struct {
	*runtimeTestClient
	titles []session.SessionTitle
	err    error
}

func (c *runtimeTitleTestClient) RenameSession(_ context.Context, title session.SessionTitle) error {
	c.titles = append(c.titles, title)
	return c.err
}

func runtimeTitleUI(t *testing.T) (*appServerUI, *runtimeTitleTestClient) {
	t.Helper()
	u, base := runtimeTestUI(t)
	c := &runtimeTitleTestClient{runtimeTestClient: base}
	u.runtime.client = c
	return u, c
}

func runtimeTitleReceipt(t *testing.T, u *appServerUI, title session.SessionTitle, failed bool) {
	t.Helper()
	runtimeEvidenceEvent(t, u, session.Event{Kind: "title", Title: &title, Failed: failed, Text: "save unavailable"})
}

func TestNativeRuntimeTitleComposerConfirmation(t *testing.T) {
	for _, busy := range []bool{false, true} {
		t.Run(fmt.Sprint(busy), func(t *testing.T) {
			u, c := runtimeTitleUI(t)
			u.title = "Saved title"
			u.runtime.busy = busy
			if busy {
				u.turn, u.status = "active-turn", "Working"
			}
			runtimeKeys(t, u, "/title 修復 session title\r")
			if len(c.titles) != 1 || c.titles[0].ID == "" || c.titles[0].SessionID != "native-session" || c.titles[0].Title != "修復 session title" || len(c.sent) != 0 || u.draft != "" || u.title != "Saved title" {
				t.Fatalf("rename before receipt: titles=%+v draft=%q saved=%q sent=%q", c.titles, u.draft, u.title, c.sent)
			}
			if !strings.Contains(u.mainHeaderRight(120, true), "修復 session title") {
				t.Fatal("pending manual title is not displayed")
			}
			runtimeTitleReceipt(t, u, session.SessionTitle{ID: "unknown", Title: "Wrong title"}, false)
			if u.title != "Saved title" || len(u.titleRequests) != 1 {
				t.Fatal("unknown receipt changed confirmed title or consumed request")
			}
			runtimeTitleReceipt(t, u, c.titles[0], false)
			if u.title != "修復 session title" || len(u.titleRenames) != 0 || len(u.titleRequests) != 0 {
				t.Fatal("native receipt did not confirm title")
			}
			if busy && (!u.runtime.busy || u.turn != "active-turn" || u.status != "Working") {
				t.Fatal("rename disturbed running task")
			}
		})
	}
}

func TestNativeRuntimeTitleSaveFailureNonfatal(t *testing.T) {
	for _, immediate := range []bool{false, true} {
		t.Run(fmt.Sprint(immediate), func(t *testing.T) {
			u, c := runtimeTitleUI(t)
			u.title, u.turn, u.status, u.runtime.busy = "Saved", "active", "Working", true
			if immediate {
				c.err = errors.New("write unavailable")
			}
			runtimeKeys(t, u, "/title Unsaved\r")
			if !immediate {
				runtimeTitleReceipt(t, u, c.titles[0], true)
			}
			if u.title != "Saved" || len(u.titleRequests) != 0 || len(u.titleRenames) != 0 || u.notice == "" || u.noticeAlert || !u.dirty || !u.runtime.busy || u.turn != "active" || u.status != "Working" {
				t.Fatalf("nonfatal failure: title=%q notice=%q turn=%q status=%q", u.title, u.notice, u.turn, u.status)
			}
		})
	}
}

func TestNativeRuntimeTitleBeforeIdentity(t *testing.T) {
	u, c := runtimeTitleUI(t)
	u.thread = ""
	runtimeKeys(t, u, "/title First\r/title Final\r")
	if len(c.titles) != 0 || u.pendingTitle != "Final" || !strings.Contains(u.mainHeaderRight(120, true), "Final") {
		t.Fatal("pre-identity rename lost or sent without identity")
	}
	runtimeEvidenceEvent(t, u, session.Event{Kind: "session", SessionID: "created-session"})
	if len(c.titles) != 1 || c.titles[0].SessionID != "created-session" || c.titles[0].Title != "Final" || u.pendingTitle != "" || u.title != "" {
		t.Fatalf("identity rename: %+v pending=%q saved=%q", c.titles, u.pendingTitle, u.title)
	}
	runtimeTitleReceipt(t, u, c.titles[0], false)
	if u.title != "Final" {
		t.Fatal("pending title did not confirm")
	}
}

func TestNativeRuntimeTitleCapabilityDiscovery(t *testing.T) {
	for _, capable := range []bool{false, true} {
		t.Run(fmt.Sprint(capable), func(t *testing.T) {
			u, base := runtimeTestUI(t)
			if capable {
				u.runtime.client = &runtimeTitleTestClient{runtimeTestClient: base}
			}
			runtimeKeys(t, u, "/")
			if slices.ContainsFunc(u.picker.choices, func(c composerChoice) bool { return c.name == "/title" }) != capable {
				t.Fatal("title picker does not match client capability")
			}
			if !capable {
				runtimeKeys(t, u, "title Unsupported\r")
				if len(base.sent) != 0 || len(u.titleRequests) != 0 || len(u.titleRenames) != 0 || u.notice == "" || u.noticeAlert {
					t.Fatal("unsupported title used native prompt or pending save")
				}
			}
		})
	}
}

func TestUISnapshotNativeRuntimeRenamedTitle(t *testing.T) {
	for _, width := range []int{42, 120} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			u, c := runtimeTitleUI(t)
			runtimeKeys(t, u, "/title 修復 native session title\r")
			runtimeTitleReceipt(t, u, c.titles[0], false)
			uisnapshot.Assert(t, filepath.Join("testdata", "snapshots", fmt.Sprintf("native-runtime-renamed-title-%d.txt", width)), runtimeFrame(t, u, width, 28))
		})
	}
}
