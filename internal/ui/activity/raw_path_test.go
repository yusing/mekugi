package activity_test

import (
	"testing"

	activityui "github.com/yusing/mekugi/internal/ui/activity"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func TestUISnapshotRawFilePaths(t *testing.T) {
	p := activityui.Painter{CopySource: true}
	text := "Read /tmp/source.go:26, src/source.go:2-3 and `src/file name.go`.\nRead README.md, ./source and ../source.\nKeep `cat src/source.go` and `fmt.Println(value)` as code.\n\nJournal /1/2 and agent /root/worker remain identifiers.\n\n```text\n/tmp/literal.go\n```"
	rows := p.Markdown(text, 42)
	uisnapshot.AssertTerminal(t, "testdata/snapshots/raw-file-paths.txt", append(rows, "plain after paths"), 42)
}
