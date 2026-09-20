package router

import (
	"encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi"
)

func TestJournalChildCompletionIncludesOwnChanges(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	proxy.replayStore = store
	child, _ := prepareActivityTest(t, proxy, "child", "child", "root", "/root/child", nil)
	defer child.Close()

	seed := func(stream, call, executing, path, before, after string) string {
		t.Helper()
		id, err := store.reserveChange(t.Context(), child.directory, stream, call)
		if err != nil {
			t.Fatal(err)
		}
		history := mekugiHistory{
			ChangeID: id, CorrelationID: call, ExecutingThread: executing, Applied: true,
			ReviewFiles: []mekugi.ReviewFile{mekugi.RenderReviewFile(path, path, before, after)},
		}
		if err := store.put(t.Context(), child.directory, map[string]mekugiHistory{call: history}); err != nil {
			t.Fatal(err)
		}
		return id
	}

	owner := child.shellThreadID
	first := seed(owner, "first", owner, "first.txt", "old\n", "new\n")
	hole := seed(owner, "foreign-attempt", "other", "excluded.txt", "x\n", "y\n")
	third := seed(owner, "third", owner, "third.txt", "", "one\ntwo\n")
	seed(owner, "fourth", owner, "fourth.txt", "one\n", "two\n")
	fifth := seed(owner, "fifth", owner, "fifth.txt", "one\n", "")
	foreign := seed("other", "other", "other", "foreign.txt", "", "foreign\n")
	descendant := seed("nested", "nested", "nested", "nested.txt", "", "nested\n")
	recovery := seed("recovery-stream", "recovered", owner, "recovery.txt", "a\n", "b\nc\n")

	// A later attempt for an owned change was executed by another thread. Its
	// content must not be attributed to the child.
	if err := store.put(t.Context(), child.directory, map[string]mekugiHistory{"foreign-recovery": {
		ChangeID: first, CorrelationID: "first", ExecutingThread: "other", Attempt: 2, Applied: true,
		ReviewFiles: []mekugi.ReviewFile{mekugi.RenderReviewFile("leak.txt", "leak.txt", "", "leak\n")},
	}}); err != nil {
		t.Fatal(err)
	}

	result := finishChildJournalForChanges(t, child)
	for _, want := range []string{
		"Journal result", "**Changes:**", first, third + ".." + fifth, recovery,
		"1\t1\tfirst.txt", "2\t0\tthird.txt", "1\t1\tfourth.txt",
		"0\t1\tfifth.txt", "2\t1\trecovery.txt",
	} {
		if !strings.Contains(result, want) {
			t.Fatalf("completion missing %q:\n%s", want, result)
		}
	}
	for _, unwanted := range []string{hole, foreign, descendant, "excluded.txt", "foreign.txt", "nested.txt", "leak.txt"} {
		if strings.Contains(result, unwanted) {
			t.Fatalf("completion included foreign change %q:\n%s", unwanted, result)
		}
	}
	positions := []int{
		strings.Index(result, first),
		strings.Index(result, third+".."+fifth),
		strings.Index(result, recovery),
	}
	for i := 1; i < len(positions); i++ {
		if positions[i-1] < 0 || positions[i] <= positions[i-1] {
			t.Fatalf("change ranges are not in stable stream order: %v\n%s", positions, result)
		}
	}
}

func TestJournalChildCompletionPartiallyRetiredForeignChangeUnavailable(t *testing.T) {
	for _, withOwn := range []bool{false, true} {
		t.Run(map[bool]string{false: "foreign-only", true: "with-owned"}[withOwn], func(t *testing.T) {
			proxy := newManagedMekugiProxy(t)
			store, err := openMekugiReplayStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			proxy.replayStore = store
			child, _ := prepareActivityTest(t, proxy, "child", "child", "root", "/root/child", nil)
			defer child.Close()

			partial, err := store.reserveChange(t.Context(), child.directory, child.shellThreadID, "partial")
			if err != nil {
				t.Fatal(err)
			}
			if err := store.put(t.Context(), child.directory, map[string]mekugiHistory{"foreign-survivor": {
				ChangeID: partial, CorrelationID: "partial", ExecutingThread: "other", Applied: true,
				ReviewFiles: []mekugi.ReviewFile{mekugi.RenderReviewFile("foreign.txt", "foreign.txt", "", "foreign\n")},
			}}); err != nil {
				t.Fatal(err)
			}
			if withOwn {
				id, err := store.reserveChange(t.Context(), child.directory, child.shellThreadID, "owned")
				if err != nil {
					t.Fatal(err)
				}
				if err := store.put(t.Context(), child.directory, map[string]mekugiHistory{"owned": {
					ChangeID: id, CorrelationID: "owned", ExecutingThread: child.shellThreadID, Applied: true,
					ReviewFiles: []mekugi.ReviewFile{mekugi.RenderReviewFile("owned.txt", "owned.txt", "", "owned\n")},
				}}); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.locked(t.Context(), func() error {
				index, err := store.readChangeIndex(child.directory)
				if err != nil {
					return err
				}
				change := index.Changes[partial]
				change.RetiredCalls = 1
				index.Changes[partial] = change
				return store.writeChangeIndex(index)
			}); err != nil {
				t.Fatal(err)
			}

			result := finishChildJournalForChanges(t, child)
			unavailable := "Changes unavailable"
			if withOwn {
				unavailable = "Stat unavailable"
			}
			if !strings.Contains(result, unavailable) ||
				strings.Contains(result, "No recorded changes.") ||
				strings.Contains(result, "Aggregated numstat") {
				t.Fatalf("partially retired evidence claimed a complete result:\n%s", result)
			}
		})
	}
}

func TestJournalChildCompletionChangesEmptyUnavailableAndRestart(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		proxy := newManagedMekugiProxy(t)
		var err error
		proxy.replayStore, err = openMekugiReplayStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		child, _ := prepareActivityTest(t, proxy, "child", "child", "root", "/root/child", nil)
		defer child.Close()
		result := finishChildJournalForChanges(t, child)
		if !strings.Contains(result, "**Changes:**") || !strings.Contains(result, "No recorded changes.") {
			t.Fatalf("empty changes result:\n%s", result)
		}
	})

	t.Run("unavailable", func(t *testing.T) {
		proxy := newManagedMekugiProxy(t)
		child, _ := prepareActivityTest(t, proxy, "child", "child", "root", "/root/child", nil)
		defer child.Close()
		proxy.replayStore = nil
		result := finishChildJournalForChanges(t, child)
		if !strings.Contains(result, "**Changes:**") || !strings.Contains(result, "Changes unavailable") {
			t.Fatalf("missing-store result:\n%s", result)
		}
	})

	t.Run("corrupt", func(t *testing.T) {
		proxy := newManagedMekugiProxy(t)
		store, err := openMekugiReplayStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		proxy.replayStore = store
		child, _ := prepareActivityTest(t, proxy, "child", "child", "root", "/root/child", nil)
		defer child.Close()
		if err := os.WriteFile(filepath.Join(store.directory, changeIndexName(child.directory)), []byte("{"), 0600); err != nil {
			t.Fatal(err)
		}
		result := finishChildJournalForChanges(t, child)
		if !strings.Contains(result, "Changes unavailable") {
			t.Fatalf("corrupt evidence result:\n%s", result)
		}
	})

	t.Run("restart", func(t *testing.T) {
		proxy := newManagedMekugiProxy(t)
		directory := t.TempDir()
		store, err := openMekugiReplayStore(directory)
		if err != nil {
			t.Fatal(err)
		}
		proxy.replayStore = store
		child, _ := prepareActivityTest(t, proxy, "initial", "child", "root", "/root/child", nil)
		workspace := child.directory
		id, err := store.reserveChange(t.Context(), workspace, "original-stream", "edit")
		if err != nil {
			t.Fatal(err)
		}
		if err := store.put(t.Context(), workspace, map[string]mekugiHistory{"edit": {
			ChangeID: id, CorrelationID: "edit", ExecutingThread: child.shellThreadID, Applied: true,
			ReviewFiles: []mekugi.ReviewFile{mekugi.RenderReviewFile("durable.txt", "durable.txt", "", "kept\n")},
		}}); err != nil {
			t.Fatal(err)
		}
		child.Close()

		proxy.activity = newSubagentActivity()
		proxy.journals = newJournalStore()
		proxy.replayStore, err = openMekugiReplayStore(directory)
		if err != nil {
			t.Fatal(err)
		}
		child, _ = prepareActivityTest(t, proxy, "resumed", "child", "root", "", nil)
		defer child.Close()
		result := finishChildJournalForChanges(t, child)
		if !strings.Contains(result, id) || !strings.Contains(result, "1\t0\tdurable.txt") {
			t.Fatalf("restart lost durable changes:\n%s", result)
		}
	})
}

func finishChildJournalForChanges(t *testing.T, child *mekugiResponseTransform) string {
	t.Helper()
	requestJournalFinish(t, child)
	wire, err := child.TransformJSON([]byte(`{"id":"child-result","status":"completed","output":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	child.Delivered(wire)
	child.ReleaseDelivery()
	var response struct {
		Output []struct {
			Phase   string `json:"phase"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(wire, &response); err != nil {
		t.Fatal(err)
	}
	var result string
	for _, item := range response.Output {
		if item.Phase != "final_answer" {
			continue
		}
		for _, content := range item.Content {
			if content.Type == "output_text" {
				result += content.Text
			}
		}
	}
	if result == "" {
		t.Fatalf("completion has no final answer: %s", wire)
	}
	return result
}
