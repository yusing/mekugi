package router

import (
	"bytes"
	"strings"
	"testing"
)

func TestNativeJournalReceiptRequiresRenderedRevision(t *testing.T) {
	transform, proxy, _, workspace := newMekugiTestTransform(t)
	sink := proxy.journals.attachNative(workspace, "thread-1")
	defer proxy.journals.detachNative(sink)
	add := func(receipt, id, text string, report bool) {
		t.Helper()
		mutation := journalMutation{Op: "add", Text: &text, ReportNow: report}
		if id != "" {
			mutation = journalMutation{Op: "edit", ID: id, Text: &text, ReportNow: report}
		}
		if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "thread-1", receipt, []journalMutation{mutation}); err != nil {
			t.Fatal(err)
		}
	}
	add("first", "", "First", true)
	first := sink.snapshot()
	if len(first) != 1 || first[0].item.Text != "First" || first[0].terminal {
		t.Fatalf("native report_now publication: %+v", first)
	}
	items, err := proxy.journals.list(t.Context(), proxy.replayStore, workspace, "thread-1")
	if err != nil || items[0].Reported {
		t.Fatalf("snapshot acknowledged unrendered item: %+v, %v", items, err)
	}
	add("edit", first[0].item.ID, "Revised", true)
	if err := sink.acknowledge(t.Context(), proxy, first); err != nil {
		t.Fatal(err)
	}
	items, err = proxy.journals.list(t.Context(), proxy.replayStore, workspace, "thread-1")
	if err != nil || items[0].Reported || items[0].Text != "Revised" {
		t.Fatalf("stale render consumed newer revision: %+v, %v", items, err)
	}
	current := sink.snapshot()
	if len(current) != 1 || current[0].item.Text != "Revised" {
		t.Fatalf("new revision was discarded: %+v", current)
	}
	if err := sink.acknowledge(t.Context(), proxy, current); err != nil {
		t.Fatal(err)
	}
	items, err = proxy.journals.list(t.Context(), proxy.replayStore, workspace, "thread-1")
	if err != nil || !items[0].Reported || items[0].Flushed || len(sink.snapshot()) != 0 {
		t.Fatalf("render receipt: %+v, pending=%+v, err=%v", items, sink.snapshot(), err)
	}
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "thread-1", "delete", []journalMutation{{Op: "delete", ID: current[0].item.ID, ReportNow: true}}); err != nil {
		t.Fatal(err)
	}
	retraction := sink.snapshot()
	if len(retraction) != 1 || !retraction[0].retracted || retraction[0].item.ID != current[0].item.ID {
		t.Fatalf("visible deletion did not publish retraction: %+v", retraction)
	}
	if err := sink.acknowledge(t.Context(), proxy, retraction); err != nil {
		t.Fatal(err)
	}
	if len(sink.snapshot()) != 0 {
		t.Fatalf("acknowledged retraction remained pending: %+v", sink.snapshot())
	}
	_ = transform
}

func TestNativeJournalSinkIsolationAndDetach(t *testing.T) {
	_, proxy, _, workspace := newMekugiTestTransform(t)
	main := proxy.journals.attachNative(workspace, "thread-1")
	other := proxy.journals.attachNative(workspace, "other")
	defer proxy.journals.detachNative(other)
	text := "Independent milestone"
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "thread-1", "main-add", []journalMutation{{Op: "add", Text: &text, ReportNow: true}}); err != nil {
		t.Fatal(err)
	}
	if len(main.snapshot()) != 1 || len(other.snapshot()) != 0 || proxy.journals.nativeSink(workspace, "other") != other {
		t.Fatalf("publication crossed native thread: main=%+v other=%+v", main.snapshot(), other.snapshot())
	}
	proxy.journals.detachNative(main)
	if proxy.journals.nativeSink(workspace, "thread-1") != nil {
		t.Fatal("detached native sink remains attached")
	}
	items, err := proxy.journals.list(t.Context(), proxy.replayStore, workspace, "thread-1")
	if err != nil || len(items) != 1 || items[0].Reported || items[0].Flushed {
		t.Fatalf("detach acknowledged undelivered item: %+v, %v", items, err)
	}
}

func TestNativeJournalFinalKeepsProviderAndPublishesOnlyAfterDelivery(t *testing.T) {
	transform, proxy, _, workspace := newMekugiTestTransform(t)
	sink := proxy.journals.attachNative(workspace, "thread-1")
	defer proxy.journals.detachNative(sink)
	transform.journalQuestion = "What was done?"
	answer := map[string]any{"type": "message", "id": "provider-final", "role": "assistant", "phase": "final_answer", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "Completed work."}}}
	response := mustTestJSON(t, map[string]any{"id": "response-final", "status": "completed", "output": []any{answer}})
	output, err := transform.TransformJSON(response)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(output, []byte(`"provider-final"`)) || bytes.Contains(output, []byte("Journal flush")) || bytes.Contains(output, []byte("Journal update")) {
		t.Fatalf("native terminal replaced provider result or generated carrier: %s", output)
	}
	if len(sink.snapshot()) != 0 {
		t.Fatalf("terminal published before host delivery: %+v", sink.snapshot())
	}
	items, err := proxy.journals.list(t.Context(), proxy.replayStore, workspace, "thread-1")
	if err != nil || len(items) != 1 || items[0].Question != "What was done?" || items[0].Text != "Completed work." || items[0].Flushed {
		t.Fatalf("captured answer metadata: %+v, %v", items, err)
	}
	if !sink.hides("provider-final") || sink.hides("unrelated-final") {
		t.Fatal("native sink hid a provider item other than the captured answer")
	}
	transform.Delivered(output)
	transform.ReleaseDelivery()
	pending := sink.snapshot()
	if len(pending) != 1 || !pending[0].terminal || pending[0].item.ID != items[0].ID {
		t.Fatalf("completed delivery did not publish typed answer: %+v", pending)
	}
	if err := sink.acknowledge(t.Context(), proxy, pending); err != nil {
		t.Fatal(err)
	}
	items, err = proxy.journals.list(t.Context(), proxy.replayStore, workspace, "thread-1")
	if err != nil || !items[0].Reported || !items[0].Flushed {
		t.Fatalf("render acknowledgement not persisted: %+v, %v", items, err)
	}
	view := newLiveActivityView()
	view.applyJournal("thread-1", pending[0])
	if len(view.entries) != 1 || view.entries[0].journal == nil || view.entries[0].journal.Question != "What was done?" || strings.Contains(view.entries[0].Text, "**Question:**") || strings.Contains(view.entries[0].Text, "Journal flush") {
		t.Fatalf("typed Activity entry fell back to old flush format: %+v", view.entries)
	}
}
