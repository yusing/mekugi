package router

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/modules/conversation/composer"
)

func bindComposerFile(u *appServerUI, label, path string) {
	start := len(u.Text)
	u.Text += label
	u.Files = append(u.Files, composer.Token{Start: start, End: len(u.Text), Path: path})
}

func TestComposerFileTokenHistoryAndRejectedSubmission(t *testing.T) {
	u, w := newAppServerTestUI()
	u.cwd = "/work"
	appServerTestKeys(t, u, "@f")
	requests := pickerRequests(t, w)
	pickerReply(t, u, requests[0].ID, `{"files":[{"root":"/work","path":"file name.go"}]}`)
	appServerTestKeys(t, u, "\t")
	w.Reset()
	appServerTestKeys(t, u, "\r")
	if !strings.Contains(w.String(), `@\"file name.go\"`) {
		t.Fatalf("wire input lost visible @ prefix: %s", w.String())
	}
	if len(u.conversation().Submitted[u.Submission().ID].Files) != 1 || len(u.Files) != 0 {
		t.Fatalf("submission lost file binding: submitted=%+v draft=%+v", u.conversation().Submitted[u.Submission().ID].Files, u.Files)
	}
	appServerTestMessage(t, u, `{"id":2,"error":{"code":-1,"message":"rejected"}}`)
	if u.Text != `@"file name.go" ` || len(u.Files) != 1 || u.Files[0].Path != "file name.go" {
		t.Fatalf("rejection lost file token: %q %+v", u.Text, u.Files)
	}
	u.rememberInput(u.draftSnapshot())
	u.Text, u.Files = "temporary", nil
	u.recallInput(true)
	if u.Text != `@"file name.go" ` || len(u.Files) != 1 {
		t.Fatalf("history lost file token: %q %+v", u.Text, u.Files)
	}
}

func TestComposerFileTokenEditorRelocationAndIdentity(t *testing.T) {
	u, _ := newAppServerTestUI()
	bindComposerFile(u, `@"file name.go"`, "file name.go")
	u.Text += " "
	bindComposerFile(u, `@"file name.go"`, "other/file name.go")
	if err := u.applyEditorDraft(`before @"file name.go" and @"file name.go" after`); err != nil {
		t.Fatal(err)
	}
	if len(u.Files) != 2 || u.Files[0].Start != len("before ") || u.Files[1].Start <= u.Files[0].End || u.Files[0].Path != "file name.go" || u.Files[1].Path != "other/file name.go" {
		t.Fatalf("duplicate labels lost distinct file bindings: %+v", u.Files)
	}
	u.undoDraft(false)
	if len(u.Files) != 2 || u.Files[0].Start != 0 {
		t.Fatalf("editor undo lost original bindings: %q %+v", u.Text, u.Files)
	}
	if err := u.applyEditorDraft(`@"file name.go".bak`); err != nil {
		t.Fatal(err)
	}
	if len(u.Files) != 0 {
		t.Fatalf("partial filename retained old binding: %+v", u.Files)
	}
}

func TestComposerFileTokenMixedImageOffsets(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.insertImage(filepath.Join(t.TempDir(), "one.png"))
	u.insertDraft(" ")
	bindComposerFile(u, "@code.go", "code.go")
	u.insertDraft(" ")
	u.insertImage(filepath.Join(t.TempDir(), "two.png"))
	if len(u.Files) != 1 || u.Text[u.Files[0].Start:u.Files[0].End] != "@code.go" {
		t.Fatalf("file binding shifted by image insertion: %q %+v", u.Text, u.Files)
	}
	u.CursorBack = len(u.Text) - u.Images[0].End
	u.deleteDraft(true)
	if len(u.Images) != 1 || len(u.Files) != 1 || u.Text[u.Files[0].Start:u.Files[0].End] != "@code.go" {
		t.Fatalf("image removal corrupted file binding: %q %+v", u.Text, u.Files)
	}
}

func TestComposerFileTokenEditorPreservesExistingAdjacency(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.Text = "("
	bindComposerFile(u, "@file.go", "file.go")
	u.insertDraft("), before")
	if err := u.applyEditorDraft("(@file.go), after"); err != nil {
		t.Fatal(err)
	}
	if len(u.Files) != 1 || u.Files[0].Start != 1 || u.Text[u.Files[0].Start:u.Files[0].End] != "@file.go" {
		t.Fatalf("unrelated editor change lost selected token: %q %+v", u.Text, u.Files)
	}
}

func TestComposerKillLineKeepsMultilineFileAtomic(t *testing.T) {
	u, w := newAppServerTestUI()
	u.cwd = "/work"
	appServerTestKeys(t, u, "@f")
	requests := pickerRequests(t, w)
	pickerReply(t, u, requests[0].ID, `{"files":[{"root":"/work","path":"foo\nbar.txt"}]}`)
	appServerTestKeys(t, u, "\t")
	original := u.Text
	appServerTestKeys(t, u, "\x1b[D\x1b[D\x0b")
	if u.Text != " " || len(u.Files) != 0 {
		t.Fatalf("split token: %q, %+v", u.Text, u.Files)
	}
	appServerTestKeys(t, u, "\x1a")
	if u.Text != original || len(u.Files) != 1 {
		t.Fatalf("undo: %q, %+v", u.Text, u.Files)
	}
}
