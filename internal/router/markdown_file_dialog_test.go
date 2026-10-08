package router

import (
	"encoding/base64"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

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
	for _, target := range []string{"src/source #1?%.go", path + ":26", fileURL + ":26", "src/source #1?%.go:26-27", fileURL + ":26-27", "`" + path + ":26`"} {
		t.Run(target, func(t *testing.T) {
			row := (&activityui.Painter{}).Inline("[source](<" + target + ">)")
			if strings.HasPrefix(target, "`") {
				row = (&activityui.Painter{}).Inline(target)
				target = strings.Trim(target, "`")
			}
			u := selectionTestUI(row)
			u.main.session.cwd = workspace
			u.selectionMouse(0, 1, 0, false)
			u.selectionMouse(0, 1, 0, true)
			if u.output == nil || u.clipboard != "" || u.selection != nil {
				t.Fatal("file click did not open a clean dialog")
			}
			u.width, u.height = 80, 18
			frame := drawOutputDialog(u)
			if u.output.filePath != "src/source #1?%.go" || u.output.fileFirst > 0 && (u.output.top == 0 || !strings.Contains(frame, "package target")) {
				t.Fatalf("path or line destination lost: %q", frame)
			}
			if u.output.fileFirst > 0 {
				if !strings.Contains(ansi.Strip(u.output.laid.Title), target[strings.LastIndexByte(target, ':'):]) {
					t.Fatal("title lost the original location")
				}
				return // Copy and drag share one path after destination resolution.
			}
			u.outputKey("y")
			if u.clipboard != "\x1b]52;c;"+base64.StdEncoding.EncodeToString([]byte(content))+"\x07" {
				t.Fatal("whole-file copy changed source or trailing newlines")
			}
			u = selectionTestUI(row)
			u.main.session.cwd = workspace
			selectionTestDrag(t, u, 0, 0, 5, 0)
			if u.output != nil || u.clipboard != "" || u.selection.text() != "source" {
				t.Fatal("drag activated the file link")
			}
		})
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
		rows := make([]string, u.height)
		u.paintOutput(rows, u.width, u.height)
		if strings.Contains(strings.Join(rows, "\n"), control) {
			t.Fatal("file emitted a clipboard control in the rendered frame")
		}
		u.outputKey("y")
		if u.clipboard != "\x1b]52;c;"+base64.StdEncoding.EncodeToString([]byte(content))+"\x07" {
			t.Fatal("explicit source copy changed original bytes")
		}
	}
}

func TestMarkdownFileDialogViews(t *testing.T) {
	workspace := t.TempDir()
	content := "# Heading\n\n**bold** and [source](sample.go:1)\n\n" + strings.Repeat("more content\n", 40)
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
	drawOutputDialog(u)
	x, y := outputSelectionPoint(t, u, "bold")
	outputSelectionDrag(u, x, y, x+3, y)
	if u.selection == nil || u.selection.text() != "**bold**" {
		t.Fatal("rendered selection lost Markdown source formatting")
	}
	u.outputKey("\x1b") // Clear the selection.
	u.outputKey("/")
	u.outputKey("bold")
	u.outputKey("\r")
	markdownTop := u.output.top
	tab := u.output.tabs[1]
	u.outputMouse(0, u.output.rect.x+2+tab.from, u.output.rect.y+2, false)
	drawOutputDialog(u)
	if u.output.page != 1 || u.output.top == 0 || u.output.query != "" {
		t.Fatal("File tab did not reveal the source location with separate search state")
	}
	for _, key := range []string{"y", "\x1b[D", "y"} {
		u.outputKey(key)
		drawOutputDialog(u)
		if key == "y" && u.clipboard != "\x1b]52;c;"+base64.StdEncoding.EncodeToString([]byte(content))+"\x07" {
			t.Fatal("view copy changed source bytes")
		}
	}
	if u.output.query != "bold" || u.output.top != markdownTop {
		t.Fatal("view switching lost navigation")
	}
	x, y = outputSelectionPoint(t, u, "source")
	u.outputMouse(0, x, y, false)
	u.outputMouse(0, x, y, true)
	if u.output.filePath != "sample.go" || len(u.output.pages) != 1 {
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
	u := &terminalUI{main: main, width: 80, height: 24}
	u.openMarkdownFile(main.view, path)
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
	if u.output != before || u.clipboard != "" {
		t.Fatal("non-file dialog link behavior changed")
	}
	x, y = outputSelectionPoint(t, u, "sample.go:1")
	u.outputMouse(0, x, y, false)
	u.outputMouse(0, x, y, true)
	if u.output == before || u.output.filePath != "sample.go" {
		t.Fatal("dialog link did not open the file")
	}
	drawOutputDialog(u)
	x, y = outputSelectionPoint(t, u, "sample.go")
	outputSelectionDrag(u, x, y, x+8, y)
	if u.selection == nil || u.selection.text() != "sample.go" {
		t.Fatal("displayed path is not selectable")
	}
	for _, target := range []string{"missing.go", "sample.go:0", "sample.go:2-1", "sample.go:1-", workspace, "file://remote" + workspace + "/sample.go", "https://example.com", "#section"} {
		if u.openMarkdownFile(main.view, target) {
			t.Fatalf("opened non-file destination %q", target)
		}
	}
	main.session.cwd = ""
	if u.openMarkdownFile(main.view, "sample.go") {
		t.Fatal("relative file resolved without workspace metadata")
	}
	path := filepath.Join(workspace, "sample.go")
	if !u.openMarkdownFile(main.view, path) || u.output.filePath != path {
		t.Fatal("external absolute path was shortened")
	}
	path += ":1-2"
	if err := os.WriteFile(path, []byte("literal file name\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if !u.openMarkdownFile(main.view, path) || u.output.filePath != path || u.output.fileFirst != 0 {
		t.Fatal("location parsing took precedence over an exact file name")
	}
}

func TestRawFileLinkFromJournalPane(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.ensureShell()
	t.Cleanup(u.shell.diff.close)
	u.session.cwd = t.TempDir()
	if err := os.WriteFile(filepath.Join(u.session.cwd, "sample.go"), []byte("package sample\n"), 0600); err != nil {
		t.Fatal(err)
	}
	journal := nativeJournalFixture()
	journal.Items[0].Title = "Read sample.go:1"
	u.journal = &nativeJournalSink{tree: &journal}
	u.shell.journalOpen = true
	rows := nativeJournalPaintedPane(t, u)
	r := u.shell.layout.journal
	for y, row := range rows {
		if x := strings.Index(ansi.Strip(row), "sample.go:1"); x >= 0 {
			nativeJournalMouse(t, u, 0, r.x+x, r.y+y)
			if u.shell.output == nil || u.shell.output.filePath != "sample.go" {
				t.Fatal("journal path click did not open the file")
			}
			return
		}
	}
	t.Fatal("journal file path was not rendered")
}

func TestUISnapshotMarkdownFileDialog(t *testing.T) {
	for _, test := range []struct {
		name, content, location string
		theme                   livediff.Theme
	}{
		{"native-markdown-file-dialog", "package sample\n\n// Copy this source. " + strings.Repeat("more source ", 8) + "\n", ":1-3", livediff.TerminalTheme},
		{"native-markdown-file-error", "\xff", ":1-3", livediff.TerminalTheme},
		{"native-markdown-file-line-52-dark", strings.Repeat("// previous line\n", 50) + "// before\nvar text = \"green\" // " + strings.Repeat("wrapped source ", 8) + "\n// selected too\n// after\n", ":52-53", livediff.DarkTheme},
		{"native-markdown-file-line-52-light", strings.Repeat("// previous line\n", 50) + "// before\nvar text = \"green\" // " + strings.Repeat("wrapped source ", 8) + "\n// selected too\n// after\n", ":52-53", livediff.LightTheme},
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
			main.view.painter.Theme = test.theme
			u := &terminalUI{main: main, width: 80, height: 18}
			if !u.openMarkdownFile(main.view, path+test.location) {
				t.Fatal("existing file did not open")
			}
			rows := make([]string, u.height)
			u.paintOutput(rows, u.width, u.height)
			uisnapshot.AssertTerminal(t, "testdata/snapshots/"+test.name+".txt", append(rows, "plain after file dialog"), u.width)
			if failure {
				if ansi.Strip(u.output.laid.Lines[2].Text) != "file is not UTF-8 text" {
					t.Fatal("read error literal content changed")
				}
				u.outputKey("y")
				if u.clipboard != "\x1b]52;c;"+base64.StdEncoding.EncodeToString([]byte("file is not UTF-8 text"))+"\x07" {
					t.Fatal("read error is not copyable")
				}
			}
		})
	}
}
