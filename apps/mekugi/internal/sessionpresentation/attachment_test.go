package sessionpresentation

import (
	"slices"
	"testing"

	"github.com/yusing/mekugi/contracts/scope"
)

func TestAttachmentRetiresScopesWithoutRevivingClosedTurns(t *testing.T) {
	var attached Attachment
	var feature scope.Key[string]
	root := attached.Scope()
	*feature.Value(root) = "retained presentation"
	attached.Start("main")
	first := attached.Transcript("child", "without-start")
	if first.Thread == nil || first.Turn == nil || attached.Scope() != root {
		t.Fatal("observation did not acquire isolated thread/turn state")
	}
	attached.CloseTurn("child", "without-start")
	if attached.Transcript("child", "without-start").Turn != nil || *feature.Value(root) != "retained presentation" {
		t.Fatal("closed turn revived or discarded completed presentation")
	}
	if attached.Transcript("child", "next").Turn == nil || attached.FindThread("child") != first.Thread {
		t.Fatal("closing a turn retired its thread")
	}
	attached.Retire()
	attached.Start("late")
	if attached.Scope() != nil || attached.Thread("late") != nil || attached.Transcript("late", "turn").Turn != nil || len(slices.Collect(attached.Threads())) != 0 {
		t.Fatal("late observations repopulated the retired attachment")
	}
	var replacement Attachment
	replacement.Start("main")
	if replacement.Scope() == root || *feature.Value(replacement.Scope()) != "" || replacement.Thread("child") == first.Thread {
		t.Fatal("replacement retained previous feature state")
	}
}
