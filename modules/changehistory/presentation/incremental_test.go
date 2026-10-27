package presentation

import (
	"bytes"
	json "encoding/json/v2"
	"fmt"
	"reflect"
	"testing"

	"github.com/yusing/mekugi/libraries/diffmodel"
)

func TestIncrementalPublicationMatchesCanonicalHistory(t *testing.T) {
	var snapshot *Snapshot
	var captures []Chunk
	steps := []Chunk{
		liveDiffNetCapture(1, "/root", "/w/a", "/w/a", "old\n", "draft\n"),
		liveDiffNetCapture(2, "/root/worker", "/w/b", "/w/b", "one\n", "two\n"),
		liveDiffNetCapture(2, "/root", "/w/a", "/w/a", "draft\n", "new\n"),
		liveDiffNetCapture(3, "/root", "/w/a", "/w/c", "new\n", "new\n"),
		liveDiffNetCapture(4, "/root", "/w/a", "/w/a", "other\n", "again\n"),
		liveDiffNetCapture(5, "/root", "/w/c", "", "new\n", ""),
		liveDiffNetCapture(6, "/root", "", "/w/c", "", "created\n"),
		liveDiffNetCapture(1, "/root", "/w/late", "/w/late", "old\n", "late\n"),
		liveDiffNetCapture(7, "/root", "/tmp/external", "/tmp/external", "old\n", "new\n"),
	}
	for i, chunk := range steps {
		chunk.Key = fmt.Sprint(i)
		var prior Publication
		if snapshot != nil {
			prior = snapshot.Publication
		}
		builder := NewBuilder(snapshot)
		frozen, err := json.Marshal(prior, json.Deterministic(true))
		if err != nil {
			t.Fatal(err)
		}
		if err := builder.Add(snapshotAttempt(fmt.Sprint(i), "thread", fmt.Sprint(i), chunk)); err != nil {
			t.Fatal(err)
		}
		captures = append(captures, chunk)
		want := prior.With(GroupCaptures(captures))
		snapshot = builder.Finish()
		if !reflect.DeepEqual(snapshot.Publication, want) {
			t.Fatalf("incremental publication diverged at step %d: got=%+v want=%+v", i, snapshot.Publication, want)
		}
		if i > 0 {
			after, err := json.Marshal(builder.base.Publication, json.Deterministic(true))
			if err != nil || !bytes.Equal(after, frozen) {
				t.Fatal("publication mutated its predecessor", err)
			}
		}
	}
	prior := snapshot.Publication
	mutation := NewBuilder(snapshot)
	if err := mutation.Add(snapshotAttempt("receipt-only", "thread", "receipt")); err != nil {
		t.Fatal(err)
	}
	if got := mutation.Finish().Publication; !reflect.DeepEqual(got, prior) {
		t.Fatal("receipt-only addition changed recent highlights")
	}
	// Unknown captures must retain unknown net totals after unrelated updates.
	b := NewBuilder(snapshot)
	if err := b.Add(snapshotAttempt("unknown", "thread", "unknown", Chunk{Key: "unknown", CaptureOrder: 8, Workspace: "/w", Review: diffmodel.ReviewFile{BeforePath: "/w/unknown", AfterPath: "/w/unknown", Diff: "binary file differs", Binary: true}})); err != nil {
		t.Fatal(err)
	}
	snapshot = b.Finish()
	b = NewBuilder(snapshot)
	if err := b.Add(snapshotAttempt("later", "thread", "later", liveDiffNetCapture(9, "/root", "/w/unrelated", "/w/unrelated", "old\n", "new\n"))); err != nil {
		t.Fatal(err)
	}
	if net := b.Finish().Publication.Counts.Net; net == nil || net.Added != -1 || net.Removed != -1 {
		t.Fatalf("unknown net counts lost: %+v", net)
	}
}

func BenchmarkIncrementalSavedReview(b *testing.B) {
	for _, count := range []int{100, 500, 1000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			attempts := make([]Attempt, count)
			for i := range count {
				path := fmt.Sprintf("/w/file-%d", i)
				attempts[i] = snapshotAttempt(fmt.Sprint(i), "thread", fmt.Sprint(i), liveDiffNetCapture(uint64(i+1), "/root", path, path, "old\n", "new\n"))
			}
			b.ReportAllocs()
			for b.Loop() {
				var snapshot *Snapshot
				for _, attempt := range attempts {
					builder := NewBuilder(snapshot)
					if err := builder.Add(attempt); err != nil {
						b.Fatal(err)
					}
					snapshot = builder.Finish()
				}
				if snapshot.Len() != count {
					b.Fatal("lost attempts")
				}
			}
		})
	}
}
