package router

import (
	"encoding/base64"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/libraries/diffrender"

	"github.com/charmbracelet/x/ansi"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func awaitMarkdownFile(t *testing.T, u *terminalUI) {
	t.Helper()
	select {
	case result := <-u.markdownFileUpdates():
		if !u.loadedMarkdownFile(result) {
			t.Fatal("file result was not admitted")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for file result")
	}
}

func TestMarkdownFileLinkClickAndDrag(t *testing.T) {
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, "src"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workspace, "src", "source #1?%.go")
	content := strings.Repeat("// previous line\n", 25) + "package target\n\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	fileURL := (&url.URL{Scheme: "file", Path: path}).String()
	main, _ := newAppServerTestUI()
	main.session.cwd = workspace
	painter := activityui.Painter{FileLink: main.markdownFileAllowed}
	for _, target := range []string{"src/source #1?%.go", path + ":26", fileURL + ":26", "src/source #1?%.go:26-27", fileURL + ":26-27", "`" + path + ":26`"} {
		t.Run(target, func(t *testing.T) {
			row := painter.Inline("[source](<" + target + ">)")
			if strings.HasPrefix(target, "`") {
				row = painter.Inline(target)
				target = strings.Trim(target, "`")
			}
			u := selectionTestUI(row)
			u.main.session.cwd = workspace
			u.selectionMouse(0, 1, 0, false)
			u.selectionMouse(0, 1, 0, true)
			if u.output == nil || u.frame.Clipboard != "" || u.selection != nil {
				t.Fatal("file click did not open a clean dialog")
			}
			awaitMarkdownFile(t, u)
			u.width, u.height = 80, 18
			frame := drawOutputDialog(u)
			if u.output.file.Path != "src/source #1?%.go" || u.output.file.First > 0 && !strings.Contains(frame, "package target") {
				t.Fatalf("path or line destination lost: %q", frame)
			}
			if u.output.file.First > 0 {
				if !strings.Contains(ansi.Strip(u.output.Document.Title), target[strings.LastIndexByte(target, ':'):]) {
					t.Fatal("title lost the original location")
				}
				return // Copy and drag share one path after destination resolution.
			}
			u.outputKey("y")
			if u.frame.Clipboard != "\x1b]52;c;"+base64.StdEncoding.EncodeToString([]byte(content))+"\x07" {
				t.Fatal("whole-file copy changed source or trailing newlines")
			}
			u = selectionTestUI(row)
			u.main.session.cwd = workspace
			selectionTestDrag(t, u, 0, 0, 5, 0)
			if u.output != nil || u.frame.Clipboard != "" || u.selection.Text() != "source" {
				t.Fatal("drag activated the file link")
			}
		})
	}
}

func TestUISnapshotMarkdownFileLinks(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.ensureShell()
	u.session.cwd = t.TempDir()
	if err := os.WriteFile(filepath.Join(u.session.cwd, "sample.go"), []byte("package sample\n"), 0600); err != nil {
		t.Fatal(err)
	}
	text := "Historical replacement count or resulting speed/cost savings.\n" +
		"Missing: missing.go, `missing.go`, [missing](missing.go).\n" +
		"Existing: sample.go:1, `sample.go`, [source](sample.go:1-2).\n" +
		"Web: [remote](https://example.com)."
	for _, view := range []*activityAdapter{u.view, u.agents} {
		view.Painter.Theme = diffrender.TerminalTheme
		rows := view.Painter.Markdown(text, 72, false)
		uisnapshot.AssertTerminal(t, "testdata/snapshots/markdown-file-links.txt", append(rows, "plain after links"), 72)
	}
	u.session.cwd = ""
	if strings.Contains(u.view.Painter.Inline("sample.go"), "\x1b]8;") {
		t.Fatal("relative path became clickable without workspace metadata")
	}
}

func TestMarkdownFileDialogSanitizesDisplayKeepsSourceCopy(t *testing.T) {
	for _, size := range []int{0, 256 << 10} {
		path := filepath.Join(t.TempDir(), "data.txt")
		control := "\x1b]52;c;ZXZpbA==\x07"
		content := control + "visible\n" + strings.Repeat("a", size)
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		main, _ := newAppServerTestUI()
		u := &terminalUI{main: main, width: 80, height: 18}
		if !u.openMarkdownFile(main.view, path) {
			t.Fatal("existing file did not open")
		}
		awaitMarkdownFile(t, u)
		rows := make([]string, u.height)
		u.paintOutput(rows, u.width, u.height)
		if strings.Contains(strings.Join(rows, "\n"), control) {
			t.Fatal("file emitted a clipboard control in the rendered frame")
		}
		u.outputKey("y")
		if u.frame.Clipboard != "\x1b]52;c;"+base64.StdEncoding.EncodeToString([]byte(content))+"\x07" {
			t.Fatal("explicit source copy changed original bytes")
		}
	}
}

func TestMarkdownFileDialogViews(t *testing.T) {
	workspace := t.TempDir()
	content := "# Heading\n\n**bold** and [source](sample.go:1)\n\n" + strings.Repeat("more content\n", 25) + "source target\n" + strings.Repeat("more content\n", 14)
	path := filepath.Join(workspace, "sample.md")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "sample.go"), []byte("package sample\n"), 0600); err != nil {
		t.Fatal(err)
	}
	main, _ := newAppServerTestUI()
	main.session.cwd = workspace
	u := &terminalUI{main: main, width: 80, height: 18}
	u.openMarkdownFile(main.view, path+":30-31")
	awaitMarkdownFile(t, u)
	drawOutputDialog(u)
	x, y := outputSelectionPoint(t, u, "bold")
	outputSelectionDrag(u, x, y, x+3, y)
	if u.output.Selection == nil || u.output.Selection.Text() != "**bold**" {
		t.Fatal("rendered selection lost Markdown source formatting")
	}
	u.outputKey("\x1b") // Clear the selection.
	u.outputKey("/")
	u.outputKey("bold")
	u.outputKey("\r")
	markdownFrame := drawOutputDialog(u)
	tabX, tabY := dialogFramePoint(t, outputDialogScreen(u), " File ")
	u.outputMouse(0, tabX, tabY, false)
	fileFrame := drawOutputDialog(u)
	if u.output.Page != 1 || !strings.Contains(fileFrame, "source target") || strings.Contains(fileFrame, "**bold**") || strings.Contains(fileFrame, "/ n N find") {
		t.Fatal("File tab did not reveal the source location with separate search state")
	}
	for _, key := range []string{"y", "\x1b[D", "y"} {
		u.outputKey(key)
		drawOutputDialog(u)
		if key == "y" && u.frame.Clipboard != "\x1b]52;c;"+base64.StdEncoding.EncodeToString([]byte(content))+"\x07" {
			t.Fatal("view copy changed source bytes")
		}
	}
	if drawOutputDialog(u) != markdownFrame {
		t.Fatal("view switching lost navigation")
	}
	x, y = outputSelectionPoint(t, u, "source")
	u.outputMouse(0, x, y, false)
	u.outputMouse(0, x, y, true)
	awaitMarkdownFile(t, u)
	if u.output.file.Path != "sample.go" || len(u.output.pages) != 1 {
		t.Fatal("rendered local link did not open the source file")
	}
}

func TestUISnapshotMarkdownFileViews(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "sample.md")
	content := "# Heading\n\n**Bold** and `code`.\n\n- A list item\n\n| Name | Value |\n| --- | --- |\n| one | two |\n\n```go\npackage sample\n```\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	main, _ := newAppServerTestUI()
	main.session.cwd = workspace
	main.view.Painter.Theme = diffrender.TerminalTheme
	u := &terminalUI{main: main, width: 80, height: 24}
	u.openMarkdownFile(main.view, path)
	awaitMarkdownFile(t, u)
	for _, view := range []string{"markdown", "file"} {
		rows := make([]string, u.height)
		u.paintOutput(rows, u.width, u.height)
		uisnapshot.AssertTerminal(t, "testdata/snapshots/native-markdown-file-"+view+"-view.txt", append(rows, "plain after file view"), u.width)
		u.outputKey("\x1b[C")
	}
}

func TestMarkdownFileLinkFromDialog(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "sample.go"), []byte("package sample\n"), 0600); err != nil {
		t.Fatal(err)
	}
	main, _ := newAppServerTestUI()
	main.session.cwd = workspace
	u := &terminalUI{main: main, width: 80, height: 18}
	u.openBlocks(main.view, []activityui.Block{{Body: "Open sample.go:1 or [remote](https://example.com)."}})
	drawOutputDialog(u)
	before := u.output
	x, y := outputSelectionPoint(t, u, "remote")
	u.outputMouse(0, x, y, false)
	u.outputMouse(0, x, y, true)
	if u.output != before || u.frame.Clipboard != "" {
		t.Fatal("non-file dialog link behavior changed")
	}
	x, y = outputSelectionPoint(t, u, "sample.go:1")
	u.outputMouse(0, x, y, false)
	u.outputMouse(0, x, y, true)
	awaitMarkdownFile(t, u)
	if u.output == before || u.output.file.Path != "sample.go" {
		t.Fatal("dialog link did not open the file")
	}
	drawOutputDialog(u)
	x, y = outputSelectionPoint(t, u, "sample.go")
	outputSelectionDrag(u, x, y, x+8, y)
	if u.output.Selection == nil || u.output.Selection.Text() != "sample.go" {
		t.Fatal("displayed path is not selectable")
	}
	for _, target := range []string{"sample.go:0", "sample.go:2-1", "sample.go:1-", "file://remote" + workspace + "/sample.go", "https://example.com", "#section"} {
		if target != "https://example.com" && strings.Contains(main.view.Painter.Inline("[file](<"+target+">)"), "\x1b]8;") {
			t.Fatalf("linked non-file destination %q", target)
		}
		if u.openMarkdownFile(main.view, target) {
			t.Fatalf("opened non-file destination %q", target)
		}
	}
	for _, target := range []string{"missing.go", workspace} {
		if !strings.Contains(main.view.Painter.Inline("[file](<"+target+">)"), "\x1b]8;") {
			t.Fatalf("local destination was not linked: %q", target)
		}
		if !u.openMarkdownFile(main.view, target) {
			t.Fatalf("local destination was not admitted: %q", target)
		}
		awaitMarkdownFile(t, u)
		drawOutputDialog(u)
		errorText := ansi.Strip(u.output.Document.Lines[2].Text)
		if errorText == "" || u.output.file.Source != "" {
			t.Fatalf("local read failure did not settle to an error: %q", target)
		}
		u.outputKey("y")
		if u.frame.Clipboard != "\x1b]52;c;"+base64.StdEncoding.EncodeToString([]byte(errorText))+"\x07" {
			t.Fatalf("local read error is not copyable: %q", target)
		}
	}
	main.session.cwd = ""
	if u.openMarkdownFile(main.view, "sample.go") {
		t.Fatal("relative file resolved without workspace metadata")
	}
	path := filepath.Join(workspace, "sample.go")
	if !u.openMarkdownFile(main.view, path) {
		t.Fatal("absolute path was not admitted")
	}
	awaitMarkdownFile(t, u)
	if u.output.file.Path != path {
		t.Fatal("external absolute path was shortened")
	}
	path += ":1-2"
	if err := os.WriteFile(path, []byte("literal file name\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if !u.openMarkdownFile(main.view, path) {
		t.Fatal("literal file name was not admitted")
	}
	awaitMarkdownFile(t, u)
	if u.output.file.Path != path || u.output.file.First != 0 {
		t.Fatal("location parsing took precedence over an exact file name")
	}
}

func TestUISnapshotMarkdownFileDialog(t *testing.T) {
	for _, test := range []struct {
		name, content, location string
		theme                   diffrender.Theme
	}{
		{"native-markdown-file-dialog", "package sample\n\n// Copy this source. " + strings.Repeat("more source ", 8) + "\n", ":1-3", diffrender.TerminalTheme},
		{"native-markdown-file-error", "\xff", ":1-3", diffrender.TerminalTheme},
		{"native-markdown-file-line-52-dark", strings.Repeat("// previous line\n", 50) + "// before\nvar text = \"green\" // " + strings.Repeat("wrapped source ", 8) + "\n// selected too\n// after\n", ":52-53", diffrender.DarkTheme},
		{"native-markdown-file-line-52-light", strings.Repeat("// previous line\n", 50) + "// before\nvar text = \"green\" // " + strings.Repeat("wrapped source ", 8) + "\n// selected too\n// after\n", ":52-53", diffrender.LightTheme},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			path := filepath.Join(workspace, "sample.go")
			failure := test.content == "\xff"
			if err := os.WriteFile(path, []byte(test.content), 0600); err != nil {
				t.Fatal(err)
			}
			main, _ := newAppServerTestUI()
			main.session.cwd = workspace
			main.view.Painter.Theme = test.theme
			u := &terminalUI{main: main, width: 80, height: 18}
			if !u.openMarkdownFile(main.view, path+test.location) {
				t.Fatal("existing file did not open")
			}
			awaitMarkdownFile(t, u)
			rows := make([]string, u.height)
			u.paintOutput(rows, u.width, u.height)
			uisnapshot.AssertTerminal(t, "testdata/snapshots/"+test.name+".txt", append(rows, "plain after file dialog"), u.width)
			if failure {
				if ansi.Strip(u.output.Document.Lines[2].Text) != "file is not UTF-8 text" {
					t.Fatal("read error literal content changed")
				}
				u.outputKey("y")
				if u.frame.Clipboard != "\x1b]52;c;"+base64.StdEncoding.EncodeToString([]byte("file is not UTF-8 text"))+"\x07" {
					t.Fatal("read error is not copyable")
				}
			}
		})
	}
}
