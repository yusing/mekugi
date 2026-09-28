package router

import (
	"path/filepath"
	"strings"
	"testing"
)

func bindComposerFile(u *appServerUI, label, path string) {
	start := len(u.draft)
	u.draft += label
	u.files = append(u.files, composerFile{start: start, end: len(u.draft), path: path})
}

func TestComposerFileTokenHistoryAndRejectedSubmission(t *testing.T) {
	u, w := newAppServerTestUI()
	u.session.cwd = "/work"
	appServerTestKeys(t, u, "@f")
	requests := pickerRequests(t, w)
	pickerReply(t, u, requests[0].ID, `{"files":[{"root":"/work","path":"file name.go"}]}`)
	appServerTestKeys(t, u, "\t")
	w.Reset()
	appServerTestKeys(t, u, "\r")
	if !strings.Contains(w.String(), `@\"file name.go\"`) {
		t.Fatalf("wire input lost visible @ prefix: %s", w.String())
	}
	if len(u.submission.files) != 1 || len(u.files) != 0 {
		t.Fatalf("submission lost file binding: submitted=%+v draft=%+v", u.submission.files, u.files)
	}
	appServerTestMessage(t, u, `{"id":2,"error":{"code":-1,"message":"rejected"}}`)
	if u.draft != `@"file name.go" ` || len(u.files) != 1 || u.files[0].path != "file name.go" {
		t.Fatalf("rejection lost file token: %q %+v", u.draft, u.files)
	}
	u.rememberInput(u.draftSnapshot())
	u.draft, u.files = "temporary", nil
	u.recallInput(true)
	if u.draft != `@"file name.go" ` || len(u.files) != 1 {
		t.Fatalf("history lost file token: %q %+v", u.draft, u.files)
	}
}

func TestComposerFileTokenEditorRelocationAndIdentity(t *testing.T) {
	u, _ := newAppServerTestUI()
	bindComposerFile(u, `@"file name.go"`, "file name.go")
	u.draft += " "
	bindComposerFile(u, `@"file name.go"`, "other/file name.go")
	if err := u.applyEditorDraft(`before @"file name.go" and @"file name.go" after`); err != nil {
		t.Fatal(err)
	}
	if len(u.files) != 2 || u.files[0].start != len("before ") || u.files[1].start <= u.files[0].end || u.files[0].path != "file name.go" || u.files[1].path != "other/file name.go" {
		t.Fatalf("duplicate labels lost distinct file bindings: %+v", u.files)
	}
	u.undoDraft(false)
	if len(u.files) != 2 || u.files[0].start != 0 {
		t.Fatalf("editor undo lost original bindings: %q %+v", u.draft, u.files)
	}
	if err := u.applyEditorDraft(`@"file name.go".bak`); err != nil {
		t.Fatal(err)
	}
	if len(u.files) != 0 {
		t.Fatalf("partial filename retained old binding: %+v", u.files)
	}
}

func TestComposerFileTokenMixedImageOffsets(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.insertImage(filepath.Join(t.TempDir(), "one.png"))
	u.insertDraft(" ")
	bindComposerFile(u, "@code.go", "code.go")
	u.insertDraft(" ")
	u.insertImage(filepath.Join(t.TempDir(), "two.png"))
	if len(u.files) != 1 || u.draft[u.files[0].start:u.files[0].end] != "@code.go" {
		t.Fatalf("file binding shifted by image insertion: %q %+v", u.draft, u.files)
	}
	u.cursorBack = len(u.draft) - u.images[0].end
	u.deleteDraft(true)
	if len(u.images) != 1 || len(u.files) != 1 || u.draft[u.files[0].start:u.files[0].end] != "@code.go" {
		t.Fatalf("image removal corrupted file binding: %q %+v", u.draft, u.files)
	}
}

func TestComposerFileTokenEditorPreservesExistingAdjacency(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.draft = "("
	bindComposerFile(u, "@file.go", "file.go")
	u.insertDraft("), before")
	if err := u.applyEditorDraft("(@file.go), after"); err != nil {
		t.Fatal(err)
	}
	if len(u.files) != 1 || u.files[0].start != 1 || u.draft[u.files[0].start:u.files[0].end] != "@file.go" {
		t.Fatalf("unrelated editor change lost selected token: %q %+v", u.draft, u.files)
	}
}
