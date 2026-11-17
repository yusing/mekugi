package launch

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const managedCatalogScript = `[ "$2" = --codex ] && [ -f workspace-marker ] || exit 1
if [ "$1" = list ]; then
printf '%s' '<skills><skill name="review" description="Managed &amp; scoped"/></skills>'
elif [ "$1" = get ] && [ "$3" = review ]; then
printf '%s\n' 'Actual review instructions 世界' 'Do not replace these with a name reference.'
else exit 1; fi
`

func testManagedSkills(t *testing.T, u *appServerUI, script string) {
	t.Helper()
	bin, workspace := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "skills-mgr"), []byte("#!/bin/sh\n"+script), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "workspace-marker"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	u.ctx, u.attached.cwd = t.Context(), workspace
	u.skillEnvironment = []string{"PATH=" + bin}
	t.Cleanup(func() {
		u.picker.cancelManagedSkills()
		u.managedSkills.Wait()
	})
}
func replyManagedCatalog(t *testing.T, u *appServerUI, w *appServerTestInput, native string) {
	t.Helper()
	requests := pickerRequests(t, w)
	if len(requests) != 1 || requests[0].Method != "skills/list" {
		t.Fatalf("missing catalog request: %+v", requests)
	}
	pickerReply(t, u, requests[0].ID, `{"data":[{"cwd":`+strconv.Quote(u.attached.cwd)+`,"skills":`+native+`}]}`)
	awaitManagedCatalog(t, u)
}

func awaitManagedCatalog(t *testing.T, u *appServerUI) {
	t.Helper()
	if load := u.picker.skillsLoad; load != nil {
		select {
		case <-load.done:
		case <-time.After(5 * time.Second):
			t.Fatal("managed catalog did not complete")
		}
		u.picker.pollManagedSkills(&u.attached, u.clock)
	}
	// Drive the normal post-acquisition binding and pending-input consumers.
	appServerTestMessage(t, u, `{"method":"fixture/tick"}`)
}

func TestManagedSkillSourceFailuresRemainIndependent(t *testing.T) {
	u, w := newAppServerTestUI()
	testManagedSkills(t, u, managedCatalogScript)
	appServerTestKeys(t, u, "\x1b[200~use $review please\x1b[201~\r")
	requests := pickerRequests(t, w)
	appServerTestMessage(t, u, `{"id":`+strconv.Itoa(requests[0].ID)+`,"error":{"code":-1,"message":"native catalog unavailable"}}`)
	awaitManagedCatalog(t, u)
	if !strings.Contains(u.attached.conversation.Submitted[u.Submission().ID].Text, "$review") || !u.attached.feedback.Alert || !strings.Contains(u.attached.feedback.Text, "native catalog unavailable") {
		t.Fatalf("native failure discarded managed source: %+v %q", u.attached.conversation.Submitted[u.Submission().ID], u.attached.feedback.Text)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := readManagedSkills(ctx, u.attached.cwd, u.skillEnvironment); err == nil {
		t.Fatal("canceled catalog ran")
	}
}

func TestManagedSkillFailureKeepsNativeCatalog(t *testing.T) {
	u, w := newAppServerTestUI()
	testManagedSkills(t, u, "exit 1\n")
	appServerTestKeys(t, u, "$rev")
	replyManagedCatalog(t, u, w, `[{"name":"review","path":"/native/SKILL.md","enabled":true}]`)
	if !u.picker.SkillsLoaded || len(u.picker.Choices) != 1 || u.picker.Choices[0].Path != "/native/SKILL.md" || !strings.Contains(u.attached.feedback.Text, "skills-mgr:") {
		t.Fatalf("native source lost on supplement failure: %+v, %s", u.picker.Choices, u.attached.feedback.Text)
	}
	appServerTestKeys(t, u, "iew")
	appServerTestMessage(t, u, `{"method":"fixture/tick"}`)
	if len(pickerRequests(t, w)) != 1 || u.picker.skillsLoad != nil {
		t.Fatal("failed supplement restarted without a catalog change")
	}
}
