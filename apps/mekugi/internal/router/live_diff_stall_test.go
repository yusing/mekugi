package router

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"

	"github.com/charmbracelet/x/vt"
)

// The recorded stall used a Python edit followed by a Go heredoc in one exec
// input. Exercise that producer through the native renderer while following,
// including completion without another command batch.
func TestLiveDiffCodeModeNativeSourceFollowLifecycle(t *testing.T) {
	workspace := t.TempDir()
	writeTestFile(t, workspace+"/types.go", "package p\nconst Old = 1\n")
	var source strings.Builder
	source.WriteString("package p\n")
	for i := range 80 {
		fmt.Fprintf(&source, "const Source%02d = %d\n", i, i)
	}
	command := "python3 - <<'PY'\np='types.go';s=open(p).read().replace('Old', 'New');open(p,'w').write(s)\nPY\ncat > workflow.go <<'EOF'\n" + source.String() + "EOF\n"
	input := "text(await tools.exec_command({cmd:" + strconv.Quote(command) + "}));"
	broker, sub, worker := newLiveDiffCodeModeWorkerTest(t, workspace)
	prefix := input[:strings.Index(input, "const Source49")]
	worker.appendDelta(prefix)
	partial := waitLiveDiffWorkerPreview(t, broker, sub, func(p previewmodel.Preview) bool {
		return !p.Complete && strings.Contains(fmt.Sprint(p.Files), "+const Source48")
	})
	u := nativeBatchFixture(t)
	u.shell.preview(partial, false)
	revealNativeDock(u.shell)
	screen := vt.NewEmulator(140, 48)
	defer screen.Close()
	paint := func() string {
		t.Helper()
		if err := u.paint(screen, 140, 48); err != nil {
			t.Fatal(err)
		}
		return screen.String()
	}
	if !strings.Contains(paint(), "Source48") {
		t.Fatal("partial source did not reach native renderer")
	}
	worker.finish(input)
	complete := waitLiveDiffWorkerPreview(t, broker, sub, func(p previewmodel.Preview) bool { return p.Complete })
	u.shell.preview(complete, false)
	if !strings.Contains(paint(), "Source79") {
		t.Fatal("following remained on the stalled prefix")
	}
	waitLiveDiffWorkerDone(t, worker)
}
