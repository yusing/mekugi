package router

import (
	"context"
	json "encoding/json/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const managedSkillInstructions = "Actual review instructions 世界\nDo not replace these with a name reference.\n"
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
	u.ctx, u.session.cwd = t.Context(), workspace
	u.skillEnvironment = []string{"PATH=" + bin}
}
func replyManagedCatalog(t *testing.T, u *appServerUI, w *appServerTestInput, native string) {
	t.Helper()
	requests := pickerRequests(t, w)
	if len(requests) != 1 || requests[0].Method != "skills/list" {
		t.Fatalf("missing catalog request: %+v", requests)
	}
	pickerReply(t, u, requests[0].ID, `{"data":[{"cwd":`+strconv.Quote(u.session.cwd)+`,"skills":`+native+`}]}`)
}
func TestManagedSkillAttachmentContentsAndIdentity(t *testing.T) {
	u, w := newAppServerTestUI()
	testManagedSkills(t, u, managedCatalogScript)
	appServerTestKeys(t, u, "\x1b[200~use $review then $review please\x1b[201~\r")
	// The manager owns this name even when Codex knows a same-named file.
	replyManagedCatalog(t, u, w, `[{"name":"review","path":"/stale/SKILL.md","enabled":true}]`)
	if len(u.submission.skills) != 2 || u.submission.skills[0].path != "" {
		t.Fatalf("managed identity lost: %+v", u.submission)
	}
	input := u.submission.input()
	if len(input) != 2 || input[1]["type"] != "text" {
		t.Fatalf("instructions missing or duplicated: %+v", input)
	}
	frames, ok := decodeFileAttachments(input[1]["text"].(string))
	if !ok || len(frames) != 1 || !strings.HasSuffix(frames[0], managedSkillInstructions) {
		t.Fatalf("instruction contents missing: %+v", frames)
	}
	content, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	text, spans, ok := appServerUserText(content)
	if !ok || text != "use $review then $review please" || len(spans) != 2 {
		t.Fatalf("shared transcript identity lost: %q %+v", text, spans)
	}
	blocks := appServerAttachmentBlocks(u.session.cwd, content)
	if len(blocks) != 1 || blocks[0].Verb != "Attached skill" || blocks[0].Label != "review" {
		t.Fatalf("missing receipt: %+v", blocks)
	}
}
func TestManagedSkillQueuedSnapshotAndNativeManagement(t *testing.T) {
	u, w := newAppServerTestUI()
	testManagedSkills(t, u, managedCatalogScript)
	appServerTestKeys(t, u, "$rev")
	replyManagedCatalog(t, u, w, `[]`)
	appServerTestKeys(t, u, "\x03")
	u.turn = "running"
	appServerTestKeys(t, u, "\x1b[200~use $review please\x1b[201~\t")
	if len(u.queued) != 1 || len(u.queued[0].attachments) != 1 {
		t.Fatalf("queue did not snapshot: %+v", u.queued)
	}
	if err := os.Remove(filepath.Join(strings.TrimPrefix(u.skillEnvironment[0], "PATH="), "skills-mgr")); err != nil {
		t.Fatal(err)
	}
	u.turn = ""
	if err := u.flushInput(); err != nil {
		t.Fatal(err)
	}
	frames, _ := decodeFileAttachments(u.submission.attachments[0])
	if len(frames) != 1 || !strings.HasSuffix(frames[0], managedSkillInstructions) {
		t.Fatalf("queue reopened source: %+v", frames)
	}
	u.picker.modal = "manage"
	u.filterSkills("review")
	if len(u.picker.choices) != 0 {
		t.Fatal("managed name exposed to Codex config writes")
	}
}
func TestManagedSkillReadFailureIsExplicit(t *testing.T) {
	u, w := newAppServerTestUI()
	testManagedSkills(t, u, `if [ "$1" = list ]; then printf '%s' '<skills><skill name="review"/></skills>'; else exit 1; fi`)
	appServerTestKeys(t, u, "\x1b[200~use $review please\x1b[201~\r")
	replyManagedCatalog(t, u, w, `[]`)
	content, err := json.Marshal(u.submission.input())
	if err != nil {
		t.Fatal(err)
	}
	blocks := appServerAttachmentBlocks(u.session.cwd, content)
	if u.submission.text != "use $review please" || len(blocks) != 1 || blocks[0].Verb != "Attach failed" || !strings.Contains(blocks[0].Label, "review") {
		t.Fatalf("failed skill lost prompt or receipt: %+v", blocks)
	}
}
func TestManagedSkillSourceFailuresRemainIndependent(t *testing.T) {
	u, w := newAppServerTestUI()
	testManagedSkills(t, u, managedCatalogScript)
	appServerTestKeys(t, u, "\x1b[200~use $review please\x1b[201~\r")
	requests := pickerRequests(t, w)
	appServerTestMessage(t, u, `{"id":`+strconv.Itoa(requests[0].ID)+`,"error":{"code":-1,"message":"native catalog unavailable"}}`)
	if len(u.submission.attachments) != 1 || !u.noticeAlert || !strings.Contains(u.notice, "native catalog unavailable") {
		t.Fatalf("native failure discarded managed source: %+v %q", u.submission, u.notice)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := readManagedSkills(ctx, u.session.cwd, u.skillEnvironment); err == nil {
		t.Fatal("canceled catalog ran")
	}
}
