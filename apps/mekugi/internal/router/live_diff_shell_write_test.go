package router

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
)

func TestLiveDiffPreviewWorkerCatFragmentsRetainLastDiff(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	broker, sub, worker := newLiveDiffWorkerTest(t, workspace)
	for _, delta := range []string{"cat >file.txt <<'END'\nfirst\n", "second\n", "END\n"} {
		worker.Append(delta)
		// Paced frames may show a prefix of the delta first.
		line := strings.TrimPrefix(delta, "cat >file.txt <<'END'")
		preview := waitLiveDiffWorkerPreview(t, broker, sub, func(preview previewmodel.Preview) bool {
			return preview.Status == previewmodel.PreviewEdit && len(preview.Files) == 1 &&
				(line == "END\n" || strings.Contains(preview.Files[0].Diff, "+"+strings.TrimSpace(line)))
		})
		if preview.Input != "" || !strings.Contains(preview.Files[0].Diff, "+first") {
			t.Fatalf("cat source shown instead of diff: %+v", preview)
		}
	}
	worker.Append("echo after\n")
	preview := waitLiveDiffWorkerPreview(t, broker, sub, func(preview previewmodel.Preview) bool {
		return preview.Status == previewmodel.PreviewEdit && len(preview.Files) == 1
	})
	if len(preview.Files) != 1 || !strings.Contains(preview.Files[0].Diff, "+second") || preview.Input != "" {
		t.Fatalf("unsupported suffix lost last diff: %+v", preview)
	}
	if _, err := os.Stat(filepath.Join(workspace, "file.txt")); !os.IsNotExist(err) {
		t.Fatalf("worker wrote file: %v", err)
	}
}
