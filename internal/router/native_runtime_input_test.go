package router

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/session"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

type runtimeInputTestClient struct {
	*runtimeTestClient
	inputs [][]session.InputPart
	err    error
}

func (c *runtimeInputTestClient) SendInput(_ context.Context, parts []session.InputPart) error {
	c.inputs = append(c.inputs, slices.Clone(parts))
	return c.err
}

func runtimeInputUI(t *testing.T) (*appServerUI, *runtimeInputTestClient) {
	t.Helper()
	u, base := runtimeTestUI(t)
	c := &runtimeInputTestClient{runtimeTestClient: base}
	u.runtime.client = c
	t.Cleanup(u.cancelPickerScan)
	return u, c
}

func runtimeInputImage(t *testing.T, root, name string) string {
	t.Helper()
	path := filepath.Join(root, name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func runtimeInputFile(t *testing.T, root, name, text string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

func runtimeInputScan(t *testing.T, u *appServerUI) {
	t.Helper()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for u.picker.loading {
		select {
		case result := <-u.picker.scanResults:
			u.applyPickerScan(result)
		case <-timeout.C:
			t.Fatal("native workspace completion timed out")
		}
	}
	if u.picker.problem != "" {
		t.Fatal(u.picker.problem)
	}
}

func TestNativeRuntimeInputImagesFailureAndHistory(t *testing.T) {
	u, c := runtimeInputUI(t)
	root := t.TempDir()
	first := runtimeInputImage(t, root, "first image.png")
	second := runtimeInputImage(t, root, "second.png")
	runtimeKeys(t, u, "Before 世界 ")
	runtimeKeys(t, u, "\x1b[200~"+first+"\x1b[201~")
	runtimeKeys(t, u, "between ")
	runtimeKeys(t, u, "\x1b[200~"+second+"\x1b[201~")
	runtimeKeys(t, u, "after @notes.txt $literal")
	wantDraft := "Before 世界 [Image 1] between [Image 2] after @notes.txt $literal"
	if u.draft != wantDraft || len(u.images) != 2 {
		t.Fatalf("pasted images: %q %+v", u.draft, u.images)
	}
	want := []session.InputPart{{Text: "Before 世界 "}, {ImagePath: first}, {Text: " between "}, {ImagePath: second}, {Text: " after @notes.txt $literal"}}
	before := u.draftSnapshot()
	c.err = errors.New("bridge unavailable")
	runtimeKeys(t, u, "\r")
	if !reflect.DeepEqual(u.draftSnapshot(), before) || u.runtime.busy || len(u.inputHistory) != 0 {
		t.Fatal("failed send changed draft, attachments, history, or busy state")
	}
	if len(c.inputs) != 1 || !reflect.DeepEqual(c.inputs[0], want) {
		t.Fatalf("ordered input: %+v", c.inputs)
	}
	c.err = nil
	runtimeKeys(t, u, "\r")
	if len(c.inputs) != 2 || !reflect.DeepEqual(c.inputs[1], want) || len(c.sent) != 0 || u.draft != "" || len(u.images) != 0 {
		t.Fatalf("retry did not submit exact multipart input: %+v draft=%q", c.inputs, u.draft)
	}
	if err := u.runtimeEvent(session.Event{Kind: "done"}); err != nil {
		t.Fatal(err)
	}
	runtimeKeys(t, u, "parked draft\x1b[A")
	if u.draft != wantDraft || !reflect.DeepEqual(u.images, before.images) {
		t.Fatalf("history lost images: %q %+v", u.draft, u.images)
	}
	runtimeKeys(t, u, "\x1b[B")
	if u.draft != "parked draft" || len(u.images) != 0 {
		t.Fatalf("history did not restore parked draft: %q", u.draft)
	}
	for _, path := range []string{first, second} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("user image removed: %v", err)
		}
	}
}

func TestNativeRuntimeInputImageUndo(t *testing.T) {
	u, _ := runtimeInputUI(t)
	path := runtimeInputImage(t, t.TempDir(), "image.png")
	runtimeKeys(t, u, "before ")
	runtimeKeys(t, u, "\x1b[200~"+path+"\x1b[201~")
	runtimeKeys(t, u, "\x1a")
	if u.draft != "before " || len(u.images) != 0 {
		t.Fatalf("attachment paste not one undo step: %q %+v", u.draft, u.images)
	}
	runtimeKeys(t, u, "\x19")
	if u.draft != "before [Image 1] " || len(u.images) != 1 || u.images[0].path != path {
		t.Fatalf("redo lost image: %q %+v", u.draft, u.images)
	}
}

func TestNativeRuntimeInputLocalFiles(t *testing.T) {
	u, c := runtimeInputUI(t)
	u.session.cwd = t.TempDir()
	for _, name := range []string{"node_modules/target file.txt", ".git/target", ".svn/target", ".hg/target"} {
		runtimeInputFile(t, u.session.cwd, name, "fixture")
	}
	runtimeInputFile(t, u.session.cwd, ".gitignore", "node_modules/\n")
	runtimeKeys(t, u, "Inspect @target")
	runtimeInputScan(t, u)
	if len(u.picker.choices) != 1 || u.picker.choices[0].path != "node_modules/target file.txt" {
		t.Fatalf("ignored/VCS workspace search: %+v", u.picker.choices)
	}
	runtimeKeys(t, u, "\t")
	want := "Inspect @\"node_modules/target file.txt\" "
	if u.draft != want || len(u.skills) != 0 || u.client != nil || u.proxy != nil {
		t.Fatalf("native file mention changed or used Codex transport: %q", u.draft)
	}
	runtimeKeys(t, u, "\r")
	if len(c.sent) != 1 || c.sent[0] != want || len(c.inputs) != 0 {
		t.Fatalf("plain native mention changed: %q %+v", c.sent, c.inputs)
	}
}

func TestNativeRuntimeInputImageFileCompletion(t *testing.T) {
	u, c := runtimeInputUI(t)
	u.session.cwd = t.TempDir()
	path := runtimeInputImage(t, u.session.cwd, "diagram.png")
	runtimeKeys(t, u, "@diagram")
	runtimeInputScan(t, u)
	runtimeKeys(t, u, "\t\r")
	want := []session.InputPart{{ImagePath: path}, {Text: " "}}
	if len(c.inputs) != 1 || !reflect.DeepEqual(c.inputs[0], want) || len(c.sent) != 0 {
		t.Fatalf("image completion was not native multipart: %+v %q", c.inputs, c.sent)
	}
}

func TestNativeRuntimeInputWebPComposer(t *testing.T) {
	// Valid 2x2 WebP fixture, generated by ImageMagick, not just a MIME header.
	data, err := base64.StdEncoding.DecodeString("UklGRjgAAABXRUJQVlA4ICwAAADQAQCdASoCAAIAAgA0JaACdLoB+AADsAD+8Oj3/yC5YXXI1/5sCua5+agAAA==")
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"paste", "completion"} {
		t.Run(route, func(t *testing.T) {
			u, c := runtimeInputUI(t)
			u.session.cwd = t.TempDir()
			path := filepath.Join(u.session.cwd, "fixture.webp")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			if route == "paste" {
				runtimeKeys(t, u, "\x1b[200~"+path+"\x1b[201~")
			} else {
				runtimeKeys(t, u, "@fixture")
				runtimeInputScan(t, u)
				runtimeKeys(t, u, "\t")
			}
			if u.draft != "[Image 1] " || len(u.images) != 1 || u.images[0].path != path {
				t.Fatalf("WebP not attached: %q %+v", u.draft, u.images)
			}
			runtimeKeys(t, u, "\r")
			want := []session.InputPart{{ImagePath: path}, {Text: " "}}
			if len(c.inputs) != 1 || !reflect.DeepEqual(c.inputs[0], want) || len(c.sent) != 0 {
				t.Fatal("WebP not submitted as native image input")
			}
		})
	}
}

func TestNativeRuntimeInputCommandsReplaceAndAliases(t *testing.T) {
	u, c := runtimeInputUI(t)
	u.runtimeEvent(session.Event{Kind: "ready", CommandInfo: []session.Command{{Name: "review", Description: "Review workspace", Arguments: "[scope]", Aliases: []string{"rv"}}, {Name: "compact", Builtin: true}}})
	runtimeKeys(t, u, "/rv\t")
	if u.draft != "/rv " {
		t.Fatalf("native alias completion: %q", u.draft)
	}
	runtimeKeys(t, u, "changed files\r")
	if len(c.sent) != 1 || c.sent[0] != "/rv changed files" {
		t.Fatalf("alias not forwarded unchanged: %q", c.sent)
	}
	u.runtimeEvent(session.Event{Kind: "done"})
	runtimeKeys(t, u, "/")
	u.runtimeEvent(session.Event{Kind: "commands", CommandInfo: []session.Command{{Name: "deploy", Description: "Deploy preview", Aliases: []string{"ship"}}}})
	var names []string
	for _, choice := range u.picker.choices {
		names = append(names, choice.name)
	}
	want := []string{"/copy", "/deploy", "/quit", "/session", "/ship", "/usage"}
	if !slices.Equal(names, want) {
		t.Fatalf("replacement/local commands: %q want %q", names, want)
	}
	runtimeKeys(t, u, "ship\r")
	if len(c.sent) != 2 || c.sent[1] != "/ship " {
		t.Fatalf("Enter did not complete and dispatch native alias: %q", c.sent)
	}
	u.runtimeEvent(session.Event{Kind: "done"})
	runtimeKeys(t, u, "$deploy")
	if u.picker.open || len(u.skills) != 0 {
		t.Fatal("native skill acquired a Codex dollar-token picker/binding")
	}
}

func TestNativeRuntimeInputStaleWorkspaceResults(t *testing.T) {
	u, _ := runtimeInputUI(t)
	first, second := t.TempDir(), t.TempDir()
	runtimeInputFile(t, first, "target-first.txt", "first")
	runtimeInputFile(t, second, "target-second.txt", "second")
	u.session.cwd = first
	runtimeKeys(t, u, "@target")
	oldID, oldTarget := u.picker.scanID, u.picker.target
	u.session.cwd = second
	u.refreshPicker()
	u.applyPickerScan(pickerScanResult{id: oldID, target: oldTarget, cwd: first, choices: []composerChoice{{name: "target-first.txt", path: "target-first.txt"}}})
	if len(u.picker.choices) != 0 {
		t.Fatal("old workspace result entered new cwd")
	}
	runtimeInputScan(t, u)
	if len(u.picker.choices) != 1 || u.picker.choices[0].path != "target-second.txt" {
		t.Fatalf("new workspace search: %+v", u.picker.choices)
	}
	id, target := u.picker.scanID, u.picker.target
	runtimeKeys(t, u, "-missing")
	u.applyPickerScan(pickerScanResult{id: id, target: target, cwd: second, choices: []composerChoice{{name: "target-second.txt", path: "target-second.txt"}}})
	if len(u.picker.choices) != 0 {
		t.Fatal("stale query result replaced current search")
	}
	runtimeInputScan(t, u)
	if len(u.picker.choices) != 0 {
		t.Fatalf("missing query returned choices: %+v", u.picker.choices)
	}
}

func TestUISnapshotNativeRuntimeInput(t *testing.T) {
	for _, width := range []int{42, 120} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			t.Run("commands", func(t *testing.T) {
				u, _ := runtimeInputUI(t)
				u.runtimeEvent(session.Event{Kind: "ready", CommandInfo: []session.Command{{Name: "review", Description: "Review current workspace changes", Arguments: "[scope]", Aliases: []string{"rv"}}, {Name: "compact", Description: "Compact conversation", Builtin: true}}})
				runtimeKeys(t, u, "/")
				uisnapshot.Assert(t, filepath.Join("testdata", "snapshots", fmt.Sprintf("native-runtime-input-commands-%d.txt", width)), runtimeFrame(t, u, width, 28))
			})
			t.Run("files", func(t *testing.T) {
				u, _ := runtimeInputUI(t)
				u.session.cwd = t.TempDir()
				for _, name := range []string{"src/handler.go", "src/handler_test.go", "node_modules/handler.js"} {
					runtimeInputFile(t, u.session.cwd, name, "fixture")
				}
				runtimeKeys(t, u, "Inspect @handler")
				runtimeInputScan(t, u)
				uisnapshot.Assert(t, filepath.Join("testdata", "snapshots", fmt.Sprintf("native-runtime-input-files-%d.txt", width)), runtimeFrame(t, u, width, 28))
			})
			t.Run("images", func(t *testing.T) {
				u, _ := runtimeInputUI(t)
				path := runtimeInputImage(t, t.TempDir(), "diagram.png")
				runtimeKeys(t, u, "Compare this diagram ")
				runtimeKeys(t, u, "\x1b[200~"+path+"\x1b[201~")
				runtimeKeys(t, u, "with @src/handler.go")
				// End the mention token so the snapshot isolates the attached-image composer.
				runtimeKeys(t, u, " ")
				uisnapshot.Assert(t, filepath.Join("testdata", "snapshots", fmt.Sprintf("native-runtime-input-images-%d.txt", width)), runtimeFrame(t, u, width, 28))
			})
		})
	}
}
