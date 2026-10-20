package terminalui_test

import (
	"testing"

	"github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestUISnapshotRawFilePaths(t *testing.T) {
	p := terminalui.Painter{CopySource: true, FileLink: func(string) bool { return true }}
	text := "Read /tmp/source.go:26, src/source.go:2-3 and `src/file name.go`.\nRead README.md, ./source and ../source.\nKeep `cat src/source.go` and `fmt.Println(value)` as code.\n\nAgent /root/worker remains an identifier.\n\n```text\n/tmp/literal.go\n```"
	rows := p.Markdown(text, 42, false)
	uisnapshot.AssertTerminal(t, "testdata/snapshots/raw-file-paths.txt", append(rows, "plain after paths"), 42)
}
