package launch

import (
	"strings"
	"testing"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/modules/livepreview/projection"
)

func TestJavaScriptRepeatedAssignmentThenLoopRebinding(t *testing.T) {
	directory := t.TempDir()
	command := "node - <<'JS'\nconst fs = require('node:fs');\nvar target = 'first.txt';\nfs.writeFileSync(target, 'new');\nvar target = 'second.txt';\nfor (target of ['loop.txt']) {}\nJS\n"
	files, _, recognized, err := projection.Shell(t.Context(), command, directory, true)
	if err != nil || recognized || len(files) != 0 {
		t.Fatalf("ambiguous preview binding: files=%+v recognized=%v err=%v", files, recognized, err)
	}
	intent := activitypresentation.ShellSource(command, directory)
	if !strings.HasPrefix(intent, "Edit ") || !strings.Contains(intent, "first.txt") || strings.Contains(intent, "loop.txt") {
		t.Fatalf("built write intent changed by final binding pruning: intent=%q", intent)
	}
}
