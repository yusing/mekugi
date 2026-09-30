package router

import (
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/livediff"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func TestUISnapshotJournalLiveHeading(t *testing.T) {
	transform, proxy, _, _ := newDurableTreeTransform(t)
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, transform.directory, transform.shellThreadID, "", []journalMutation{
		{Op: "add", Kind: "task", Title: new("Recover journal task IDs"), State: new("working")},
		{Op: "log", Text: new("Compact recovery excludes mounted agent results.")},
	}); err != nil {
		t.Fatal(err)
	}
	j, _, err := readThreadJournal(proxy.replayStore, transform.directory, transform.shellThreadID)
	if err != nil {
		t.Fatal(err)
	}
	messages, err := transform.prepareTreeDelivery(j, false)
	if err != nil || len(messages) != 1 {
		t.Fatalf("live delivery: %d messages, %v", len(messages), err)
	}
	painter := activityui.Painter{Theme: livediff.DarkTheme}
	uisnapshot.Assert(t, "testdata/snapshots/journal-live-heading.txt", strings.Join(painter.Markdown(commentaryMessageText(messages[0]), 70), "\n")+"\n")
	transform.Delivered(mustMarshalJSON(map[string]any{"output": messages}))
	j, _, err = readThreadJournal(proxy.replayStore, transform.directory, transform.shellThreadID)
	if err != nil || j.LiveSeq != j.Sequence {
		t.Fatalf("live header change lost acknowledgement: live=%d sequence=%d err=%v", j.LiveSeq, j.Sequence, err)
	}
}

func TestJournalLiveUpdateClipsRowLargerThanAnyUpdate(t *testing.T) {
	transform, proxy, _, _ := newDurableTreeTransform(t)
	body := strings.Repeat("xyz\n", 4000) // Indentation grows the row past one update.
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, transform.directory, transform.shellThreadID, "", []journalMutation{
		{Op: "add", Title: new("Large finding"), Body: &body},
	}); err != nil {
		t.Fatal(err)
	}
	journal, _, err := readThreadJournal(proxy.replayStore, transform.directory, transform.shellThreadID)
	if err != nil {
		t.Fatal(err)
	}
	messages, err := transform.prepareTreeDelivery(journal, false)
	if err != nil || len(messages) != 1 {
		t.Fatalf("oversized note stalled live delivery: %d messages, err=%v", len(messages), err)
	}
	text := commentaryMessageText(messages[0])
	if !strings.Contains(text, "Large finding") || !strings.Contains(text, "clipped") || len(text) > maxCommentaryPublicationBytes {
		t.Fatalf("clipped live row: %d bytes, head %q", len(text), text[:min(len(text), 80)])
	}
}

func TestJournalTurnCardWithoutContentStillSaysSo(t *testing.T) {
	card := journalTurnCard(threadJournal{Author: "/root"}, 0, false)
	if !strings.HasPrefix(card, "Journal\n\n") || !strings.Contains(card, "no open tasks") {
		t.Fatalf("empty turn card = %q", card)
	}
	withRemaining := journalTurnCard(threadJournal{Author: "/root", Items: []journalItem{{Path: "/1", ID: "/1", Kind: "task", Title: "Open", State: "pending"}}}, 0, false)
	if strings.Contains(withRemaining, "no open tasks") {
		t.Fatalf("card with remaining work claims none: %q", withRemaining)
	}
}
