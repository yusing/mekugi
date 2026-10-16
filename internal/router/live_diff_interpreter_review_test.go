package router

import (
	"strings"
	"testing"

	"github.com/yusing/mekugi/modules/livepreview/projection"
)

func TestJavaScriptRepeatedAssignmentThenLoopRebinding(t *testing.T) {
	directory := t.TempDir()
	command := "node - <<'JS'\nconst fs = require('node:fs');\nvar target = 'first.txt';\nfs.writeFileSync(target, 'new');\nvar target = 'second.txt';\nfor (target of ['loop.txt']) {}\nJS\n"
	statement := mustShellStatement(t, command)
	files, _, recognized, err := projection.Shell(t.Context(), command, directory, true)
	if err != nil || recognized || len(files) != 0 {
		t.Fatalf("ambiguous preview binding: files=%+v recognized=%v err=%v", files, recognized, err)
	}
	intent, edit := toolActivityEditStatement(command, statement)
	if !edit || !strings.Contains(intent, "first.txt") || strings.Contains(intent, "loop.txt") {
		t.Fatalf("built write intent changed by final binding pruning: edit=%v intent=%q", edit, intent)
	}
}
