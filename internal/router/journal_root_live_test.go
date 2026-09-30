package router

import (
	jsonv1 "encoding/json"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/livediff"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func TestJournalRootLiveIndependentDelivery(t *testing.T) {
	for _, tree := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "tree"}[tree], func(t *testing.T) {
			proxy := newManagedMekugiProxy(t)
			var err error
			proxy.replayStore, err = openMekugiReplayStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			root, _ := prepareActivityTest(t, proxy, "root", "root", "", "/root", nil)
			child, _ := prepareActivityTest(t, proxy, "child", "child", "root", "/root/child", nil)
			mutations := []journalMutation{{Op: "add", Text: new("Child milestone"), ReportNow: true}}
			if tree {
				mutations = []journalMutation{{Op: "add", Title: new("Child milestone")}}
			}
			if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, "", "child", "", mutations); err != nil {
				t.Fatal(err)
			}
			messages, err := child.prepareJournalDelivery(false)
			if err != nil || len(messages) != 1 {
				t.Fatalf("child live delivery: %v, %v", messages, err)
			}
			child.Delivered(mustMarshalJSON(map[string]any{"output": messages}))
			child.ReleaseDelivery()
			before, _, err := readThreadJournal(proxy.replayStore, "", "child")
			if err != nil || before.LiveSeq == 0 || before.RootLiveSeq != 0 {
				t.Fatalf("child delivery acknowledged root: %+v, %v", before, err)
			}
			messages, err = root.prepareJournalDelivery(false)
			if err != nil || len(messages) != 1 || !strings.Contains(commentaryMessageText(messages[0]), "Child milestone") {
				t.Fatalf("root missed child live update: %v, %v", messages, err)
			}
			// A failed root write leaves the window pending across a fresh router.
			root.ReleaseDelivery()
			root.Close()
			child.Close()
			proxy.journals = newJournalStore()
			proxy.activity = newSubagentActivity()
			proxy.replayStore, err = openMekugiReplayStore(proxy.replayStore.directory)
			if err != nil {
				t.Fatal(err)
			}
			root, _ = prepareActivityTest(t, proxy, "resume", "root", "", "/root", nil)
			messages, err = root.prepareJournalDelivery(false)
			if err != nil || len(messages) != 1 {
				t.Fatalf("restart lost pending root publication: %v, %v", messages, err)
			}
			root.Delivered(mustMarshalJSON(map[string]any{"output": messages}))
			root.ReleaseDelivery()
			after, _, err := readThreadJournal(proxy.replayStore, "", "child")
			if err != nil || after.RootLiveSeq != after.Sequence || after.LiveSeq != before.LiveSeq || after.FlushSeq != before.FlushSeq || after.ResultSeq != before.ResultSeq {
				t.Fatalf("root delivery altered child audience cursors: before=%+v after=%+v err=%v", before, after, err)
			}
			messages, err = root.prepareJournalDelivery(false)
			root.ReleaseDelivery()
			if err != nil || len(messages) != 0 {
				t.Fatalf("duplicate root live publication: %v, %v", messages, err)
			}
		})
	}
}

func TestJournalRootLiveScope(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	root, _ := prepareActivityTest(t, proxy, "root", "root", "", "/root", nil)
	prepareActivityTest(t, proxy, "other", "other", "", "/root", nil)
	for _, node := range []struct{ thread, parent, name string }{
		{"child", "root", "/root/child"}, {"nested", "child", "/root/child/nested"},
		{"unrelated", "other", "/root/unrelated"}, {"conflicted", "root", "/root/conflicted"},
	} {
		prepareActivityTest(t, proxy, node.thread, node.thread, node.parent, node.name, nil)
		if _, err := proxy.journals.apply(t.Context(), nil, "", node.thread, "", []journalMutation{{Op: "add", Title: new(node.thread + " milestone")}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := proxy.journals.bindIdentity(t.Context(), nil, "", "conflicted", "other", "/root/conflicted", true); err != nil {
		t.Fatal(err)
	}
	if err := proxy.journals.initialize(t.Context(), nil, "/different-workspace", "outside", "/root/outside", ""); err != nil {
		t.Fatal(err)
	}
	if err := proxy.journals.bindIdentity(t.Context(), nil, "/different-workspace", "outside", "root", "/root/outside", true); err != nil {
		t.Fatal(err)
	}
	if _, err := proxy.journals.apply(t.Context(), nil, "/different-workspace", "outside", "", []journalMutation{{Op: "add", Title: new("Outside milestone")}}); err != nil {
		t.Fatal(err)
	}
	messages, err := root.prepareJournalDelivery(false)
	root.ReleaseDelivery()
	if err != nil || len(messages) != 2 {
		t.Fatalf("root scope: %v, %v", messages, err)
	}
	for _, message := range messages {
		text := commentaryMessageText(message)
		if strings.Contains(text, "unrelated") || strings.Contains(text, "conflicted") || strings.Contains(text, "Outside") {
			t.Fatalf("root publication crossed identity/workspace scope: %s", text)
		}
	}
	// Native Main already mounts descendant journals, so do not duplicate them
	// in the provider stream or consume their fallback delivery cursor.
	proxy.journals.attachNative("", "root")
	messages, err = root.prepareJournalDelivery(false)
	root.ReleaseDelivery()
	if err != nil || len(messages) != 0 {
		t.Fatalf("native mounted child duplicated in Main response: %v, %v", messages, err)
	}
}

func TestUISnapshotJournalRootLive(t *testing.T) {
	text, _ := rootJournalLiveText(threadJournal{Author: "/root/reviewer", TreeAuthored: true, Events: []journalEvent{
		{Seq: 1, Op: "add", Path: "/1", Fields: journalNode{Kind: "note", Title: "Consumer sees the live milestone before child completion."}},
	}}, maxCommentaryPublicationBytes)
	painter := activityui.Painter{Theme: livediff.DarkTheme}
	uisnapshot.Assert(t, "testdata/snapshots/journal-root-live.txt", strings.Join(painter.Markdown(text, 70), "\n")+"\n")
}

func TestJournalRootLiveBoundedWindow(t *testing.T) {
	j := threadJournal{Author: "/root/child", TreeAuthored: true, Events: []journalEvent{
		{Seq: 1, Op: "add", Path: "/1", Fields: journalNode{Kind: "note", Title: "Large milestone", Body: strings.Repeat("xyz\n", 4000)}},
		{Seq: 2, Op: "add", Path: "/2", Fields: journalNode{Kind: "answer", Title: "Must stay terminal-only"}},
		{Seq: 3, Op: "add", Path: "/3", Fields: journalNode{Kind: "note", Title: "Later milestone"}},
	}}
	text, sequence := rootJournalLiveText(j, maxCommentaryPublicationBytes)
	if len(text) > maxCommentaryPublicationBytes || sequence != 1 || !strings.Contains(text, "clipped") {
		t.Fatalf("unbounded or lost first window: bytes=%d sequence=%d", len(text), sequence)
	}
	j.RootLiveSeq = sequence
	text, sequence = rootJournalLiveText(j, maxCommentaryPublicationBytes)
	if sequence != 3 || !strings.Contains(text, "Later milestone") || strings.Contains(text, "terminal-only") || strings.Contains(text, "Large milestone") {
		t.Fatalf("wrong continuation window: %q, %d", text, sequence)
	}
	if text, _ := rootJournalLiveText(j, 1); text != "" {
		t.Fatal("insufficient remaining budget produced an update")
	}
	j.Author = strings.Repeat("x", maxCommentaryPublicationBytes)
	if text, _ := rootJournalLiveText(j, maxCommentaryPublicationBytes); text != "" {
		t.Fatal("oversized identity produced an update")
	}
}

func TestJournalRootLiveLegacyOptIn(t *testing.T) {
	j := threadJournal{Author: "/root/child", Items: []journalItem{{Path: "/1", Updated: 1}}, Events: []journalEvent{
		{Legacy: true, Seq: 1, Op: "add", Path: "/1", Fields: journalNode{Kind: "note", Title: "Silent milestone"}},
	}}
	if text, _ := rootJournalLiveText(j, maxCommentaryPublicationBytes); text != "" {
		t.Fatalf("v1 milestone without report_now became live: %q", text)
	}
}

func TestJournalRootLiveMixedLegacyAndTree(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	root, _ := prepareActivityTest(t, proxy, "root", "root", "", "/root", nil)
	prepareActivityTest(t, proxy, "child", "child", "root", "/root/child", nil)
	if _, err := proxy.journals.apply(t.Context(), nil, "", "child", "", []journalMutation{
		{Op: "add", Text: new("Silent legacy milestone")},
		{Op: "add", Title: new("Visible tree milestone")},
	}); err != nil {
		t.Fatal(err)
	}
	messages, err := root.prepareJournalDelivery(false)
	root.ReleaseDelivery()
	if err != nil || len(messages) != 1 {
		t.Fatalf("mixed authoring live delivery: %v, %v", messages, err)
	}
	text := commentaryMessageText(messages[0])
	if strings.Contains(text, "Silent legacy milestone") || !strings.Contains(text, "Visible tree milestone") {
		t.Fatalf("v2 mutation exposed a silent v1 milestone: %s", text)
	}
}

func TestJournalRootLiveLegacyRetractionEligibility(t *testing.T) {
	for _, visible := range []bool{true, false} {
		t.Run(map[bool]string{true: "silent-edit-after-visible", false: "unseen-item-before-visible-other"}[visible], func(t *testing.T) {
			proxy := newManagedMekugiProxy(t)
			root, _ := prepareActivityTest(t, proxy, "root", "root", "", "/root", nil)
			prepareActivityTest(t, proxy, "child", "child", "root", "/root/child", nil)
			apply := func(mutations ...journalMutation) []string {
				t.Helper()
				ids, err := proxy.journals.apply(t.Context(), nil, "", "child", "", mutations)
				if err != nil {
					t.Fatal(err)
				}
				return ids
			}
			deliver := func() []map[string]jsonv1.RawMessage {
				t.Helper()
				messages, err := root.prepareJournalDelivery(false)
				if err != nil {
					t.Fatal(err)
				}
				root.Delivered(mustMarshalJSON(map[string]any{"output": messages}))
				root.ReleaseDelivery()
				return messages
			}
			id := apply(journalMutation{Op: "add", Text: new("Item A"), ReportNow: visible})[0]
			if visible {
				if messages := deliver(); len(messages) != 1 {
					t.Fatalf("missing initial visible item: %v", messages)
				}
				apply(journalMutation{Op: "edit", ID: id, Text: new("Silent revision of A")})
			} else {
				apply(journalMutation{Op: "add", Text: new("Item B"), ReportNow: true})
				if messages := deliver(); len(messages) != 1 || !strings.Contains(commentaryMessageText(messages[0]), "Item B") {
					t.Fatalf("missing unrelated visible item: %v", messages)
				}
			}
			apply(journalMutation{Op: "delete", ID: id, ReportNow: true})
			messages := deliver()
			if visible && (len(messages) != 1 || !strings.Contains(commentaryMessageText(messages[0]), "Removed")) {
				t.Fatalf("silent edit lost root retraction: %v", messages)
			}
			if !visible && len(messages) != 0 {
				t.Fatalf("root retracted an unseen item: %v", messages)
			}
		})
	}
}

func TestJournalRootLiveForkDoesNotInheritPublicationEvidence(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	root, _ := prepareActivityTest(t, proxy, "root", "root", "", "/root", nil)
	prepareActivityTest(t, proxy, "child", "child", "root", "/root/child", nil)
	ids, err := proxy.journals.apply(t.Context(), nil, "", "child", "", []journalMutation{{Op: "add", Text: new("Visible milestone"), ReportNow: true}})
	if err != nil {
		t.Fatal(err)
	}
	messages, err := root.prepareJournalDelivery(false)
	if err != nil || len(messages) != 1 {
		t.Fatalf("root live delivery: %v, %v", messages, err)
	}
	root.Delivered(mustMarshalJSON(map[string]any{"output": messages}))
	root.ReleaseDelivery()
	if err := proxy.journals.initialize(t.Context(), nil, "", "fork-with-item", "/root", "child"); err != nil {
		t.Fatal(err)
	}
	j := proxy.journals.memory[journalKey("", "fork-with-item")]
	if j.RootLiveSeq != 0 || len(j.Items) != 1 || j.Items[0].RootEverReported {
		t.Fatalf("fork borrowed source root visibility: %+v", j)
	}
	if _, err := proxy.journals.apply(t.Context(), nil, "", "child", "", []journalMutation{{Op: "delete", ID: ids[0], ReportNow: true}}); err != nil {
		t.Fatal(err)
	}
	if err := proxy.journals.initialize(t.Context(), nil, "", "fork-with-retraction", "/root", "child"); err != nil {
		t.Fatal(err)
	}
	for _, event := range proxy.journals.memory[journalKey("", "fork-with-retraction")].Events {
		if event.RootRetraction {
			t.Fatal("fork borrowed source retraction eligibility")
		}
	}
}
