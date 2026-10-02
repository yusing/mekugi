package router

import (
	"fmt"
	"reflect"
	"testing"
	"testing/synctest"
)

func nativeReceiptFixture(t *testing.T, mutations ...journalMutation) (*mekugiProxy, *nativeJournalSink) {
	t.Helper()
	replay, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store := newJournalStore()
	if err := store.initialize(t.Context(), replay, "workspace", "thread", "/root", ""); err != nil {
		t.Fatal(err)
	}
	sink := store.attachNative("workspace", "thread")
	t.Cleanup(func() { store.detachNative(sink) })
	if _, err := store.apply(t.Context(), replay, "workspace", "thread", "", mutations); err != nil {
		t.Fatal(err)
	}
	return &mekugiProxy{journals: store, replayStore: replay}, sink
}

func nativeReceiptRecord(t *testing.T, proxy *mekugiProxy) threadJournal {
	t.Helper()
	j, exists, err := readThreadJournal(proxy.replayStore, "workspace", "thread")
	if err != nil || !exists {
		t.Fatalf("read journal: exists=%v, err=%v", exists, err)
	}
	return j
}

func TestNativeJournalReceiptBatchTreeCursors(t *testing.T) {
	proxy, sink := nativeReceiptFixture(t,
		journalMutation{Op: "add", Kind: "task", Title: new("First")},
		journalMutation{Op: "add", Kind: "task", Title: new("Second")},
	)
	first := nativeReceiptRecord(t, proxy)
	sink.publish(first, true, "first-card")
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, "workspace", "thread", "", []journalMutation{
		{Op: "add", Kind: "task", Title: new("Third")},
		{Op: "add", Kind: "task", Title: new("Fourth")},
	}); err != nil {
		t.Fatal(err)
	}
	painted := sink.snapshot()
	if len(painted) != 5 {
		t.Fatalf("mixed live events and terminal card = %d, want 5", len(painted))
	}
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, "workspace", "thread", "", []journalMutation{{Op: "add", Kind: "task", Title: new("Not painted")}}); err != nil {
		t.Fatal(err)
	}
	if err := sink.acknowledge(t.Context(), proxy, painted); err != nil {
		t.Fatal(err)
	}
	j := nativeReceiptRecord(t, proxy)
	if j.LiveSeq != 4 || j.FlushSeq != 2 {
		t.Fatalf("cursor maxima: live=%d flush=%d, want 4 and 2", j.LiveSeq, j.FlushSeq)
	}
	for i, item := range j.Items {
		if item.Reported != (i < 4) || item.Flushed != (i < 2) {
			t.Fatalf("receipt crossed live/terminal window: %+v", item)
		}
	}
	if pending := sink.snapshot(); len(pending) != 1 || pending[0].item.Updated != 5 {
		t.Fatalf("unpainted event consumed: %+v", pending)
	}
}

func TestNativeJournalReceiptBatchLegacyExactRevisions(t *testing.T) {
	proxy, sink := nativeReceiptFixture(t,
		journalMutation{Op: "add", Text: new("First"), ReportNow: true},
		journalMutation{Op: "add", Text: new("Second"), ReportNow: true},
	)
	old := sink.snapshot()
	if len(old) != 2 {
		t.Fatalf("legacy publications = %d, want 2", len(old))
	}
	sink.publish(nativeReceiptRecord(t, proxy), true)
	terminal := sink.snapshot()
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, "workspace", "thread", "", []journalMutation{
		{Op: "edit", ID: old[0].item.ID, Text: new("Newer"), ReportNow: true},
		{Op: "add", Text: new("Live third"), ReportNow: true},
		{Op: "add", Text: new("Live fourth"), ReportNow: true},
	}); err != nil {
		t.Fatal(err)
	}
	painted := append([]nativeJournalPublication(nil), terminal...)
	for _, item := range sink.snapshot() {
		if item.item.ID != old[0].item.ID && item.item.ID != old[1].item.ID {
			painted = append(painted, item)
		}
	}
	if err := sink.acknowledge(t.Context(), proxy, painted); err != nil {
		t.Fatal(err)
	}
	j := nativeReceiptRecord(t, proxy)
	for _, item := range j.Items {
		switch item.Text {
		case "Newer":
			if item.Reported || item.Flushed {
				t.Fatalf("stale terminal receipt consumed newer revision: %+v", item)
			}
		case "Second":
			if !item.Reported || !item.Flushed {
				t.Fatalf("terminal receipt lost: %+v", item)
			}
		default:
			if !item.Reported || item.Flushed {
				t.Fatalf("live receipt became terminal: %+v", item)
			}
		}
	}
	if pending := sink.snapshot(); len(pending) != 1 || pending[0].item.Text != "Newer" {
		t.Fatalf("newer revision not pending: %+v", pending)
	}
}

func TestNativeJournalReceiptAsyncBlockedOwner(t *testing.T) {
	for _, gate := range []string{"state", "replay"} {
		t.Run(gate, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				proxy, sink := nativeReceiptFixture(t, journalMutation{Op: "add", Text: new("Painted"), ReportNow: true})
				painted := sink.snapshot()
				held, release, released := make(chan struct{}), make(chan struct{}), make(chan struct{})
				go func() {
					defer close(released)
					hold := func() error { close(held); <-release; return nil }
					if gate == "replay" {
						if err := proxy.replayStore.locked(t.Context(), hold); err != nil {
							t.Error(err)
						}
						return
					}
					unlock, err := proxy.journals.lockState(t.Context())
					if err != nil {
						t.Error(err)
						close(held)
						return
					}
					defer unlock()
					_ = hold()
				}()
				<-held
				defer func() { close(release); <-released; _ = sink.finishAcknowledgement(true) }()
				if got := sink.snapshot(); !reflect.DeepEqual(got, painted) {
					t.Fatal("snapshot acknowledged before painted receipt was started")
				}
				sink.startAcknowledgement(t.Context(), proxy, painted)
				synctest.Wait()
				if err := sink.finishAcknowledgement(false); err != nil {
					t.Fatal(err)
				}
				if got := sink.snapshot(); len(got) != 0 {
					t.Fatalf("in-flight exact revision visible: %+v", got)
				}
				newer := nativeReceiptRecord(t, proxy)
				newer.Sequence++
				newer.Items[0].Updated = newer.Sequence
				newer.Items[0].Text = "Newer"
				sink.publish(newer, false)
				if got := sink.snapshot(); len(got) != 1 || got[0].item.Text != "Newer" {
					t.Fatalf("newer revision hidden by in-flight receipt: %+v", got)
				}
				// A second paint must not replace or start another receipt worker.
				sink.startAcknowledgement(t.Context(), proxy, sink.snapshot())
				synctest.Wait()
				if got := sink.snapshot(); len(got) != 1 || got[0].item.Text != "Newer" {
					t.Fatalf("second worker swallowed newer publication: %+v", got)
				}
			})
		})
	}
}

func TestNativeJournalReceiptAsyncPersistenceFailure(t *testing.T) {
	proxy, sink := nativeReceiptFixture(t, journalMutation{Op: "add", Kind: "task", Title: new("Painted")})
	painted := sink.snapshot()
	before := nativeReceiptRecord(t, proxy)
	proxy.replayStore.maxBytes = 1 // Existing valid record cannot fit the receipt rewrite.
	sink.startAcknowledgement(t.Context(), proxy, painted)
	if err := sink.finishAcknowledgement(true); err == nil {
		t.Fatal("receipt persistence failure was not reported")
	}
	if got := sink.snapshot(); !reflect.DeepEqual(got, painted) {
		t.Fatalf("failed receipt discarded original publications: %+v", got)
	}
	if got := nativeReceiptRecord(t, proxy); !reflect.DeepEqual(got, before) {
		t.Fatal("failed receipt changed durable journal")
	}
	proxy.replayStore.maxBytes = 0
	sink.startAcknowledgement(t.Context(), proxy, sink.snapshot())
	if err := sink.finishAcknowledgement(true); err != nil {
		t.Fatal(err)
	}
	if len(sink.snapshot()) != 0 || nativeReceiptRecord(t, proxy).LiveSeq != before.Sequence {
		t.Fatal("retry did not persist and clear retained publication")
	}
}

func TestNativeJournalReceiptAsyncDrain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		proxy, sink := nativeReceiptFixture(t, journalMutation{Op: "add", Kind: "task", Title: new("Painted")})
		unlock, err := proxy.journals.lockState(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { unlock() }()
		sink.startAcknowledgement(t.Context(), proxy, sink.snapshot())
		// Session replacement has detached the active journal field. Its
		// already-painted receipt must still be owned and drained by the UI.
		u := &appServerUI{journalReceipts: map[*nativeJournalSink]bool{sink: true}}
		if err := u.finishJournalAcknowledgements(false); err != nil || len(u.journalReceipts) != 1 {
			t.Fatal("polling lost a blocked receipt from a replaced session")
		}
		drained := make(chan error, 1)
		go func() { drained <- u.finishJournalAcknowledgements(true) }()
		synctest.Wait()
		select {
		case err := <-drained:
			t.Fatalf("cleanup drain returned with persistence blocked: %v", err)
		default:
		}
		unlock()
		unlock = func() {}
		if err := <-drained; err != nil {
			t.Fatal(err)
		}
		proxy.journals.detachNative(sink)
		if len(sink.snapshot()) != 0 || nativeReceiptRecord(t, proxy).LiveSeq != 1 {
			t.Fatal("cleanup drain returned before receipt persisted")
		}
		if len(u.journalReceipts) != 0 {
			t.Fatal("cleanup retained a completed worker")
		}
		if err := sink.finishAcknowledgement(true); err != nil {
			t.Fatal(err)
		}
	})
}

func BenchmarkNativeJournalReceiptBatch(b *testing.B) {
	for b.Loop() {
		b.StopTimer()
		replay, err := openMekugiReplayStore(b.TempDir())
		if err != nil {
			b.Fatal(err)
		}
		store := newJournalStore()
		if err := store.initialize(b.Context(), replay, "workspace", "thread", "/root", ""); err != nil {
			b.Fatal(err)
		}
		sink := store.attachNative("workspace", "thread")
		mutations := make([]journalMutation, 30)
		for i := range mutations {
			mutations[i] = journalMutation{Op: "add", Kind: "task", Title: new(fmt.Sprintf("Task %d", i))}
		}
		if _, err := store.apply(b.Context(), replay, "workspace", "thread", "", mutations); err != nil {
			b.Fatal(err)
		}
		items := sink.snapshot()
		if len(items) != 30 {
			b.Fatalf("publications = %d, want 30", len(items))
		}
		proxy := &mekugiProxy{journals: store, replayStore: replay}
		b.StartTimer()
		if err := sink.acknowledge(b.Context(), proxy, items); err != nil {
			b.Fatal(err)
		}
		b.StopTimer()
		if len(sink.snapshot()) != 0 {
			b.Fatal("acknowledged batch remains pending")
		}
		b.StartTimer()
	}
}
