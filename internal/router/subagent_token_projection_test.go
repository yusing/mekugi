package router

import (
	"bytes"
	"testing"
)

func TestChildCompletionPreservesRootJournalAnswer(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, missing := range []bool{false, true} {
			t.Run(map[bool]string{false: "json", true: "sse"}[stream]+map[bool]string{false: "/complete", true: "/missing"}[missing], func(t *testing.T) {
				t.Setenv("TMPDIR", t.TempDir())
				proxy := newManagedMekugiProxy(t)
				root, _ := prepareActivityTest(t, proxy, "shared-session", "root", "", "/root", nil)
				child, _ := prepareActivityTest(t, proxy, "shared-session", "child", "root", "/root/worker", nil)
				child.usageTracker.model = "gpt-5.6-sol"
				root.Close()
				if missing {
					child.usageTracker.finish()
				} else {
					child.observeResponseUsage(tokenCounts{InputTokens: 120, UncachedInputTokens: 40, OutputTokens: 30, ReasoningTokens: 20})
				}
				response := []byte(`{"id":"child-response","status":"completed","output":[{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Child result."}]}]}`)
				var output []byte
				if stream {
					events, err := child.TransformSSE([]byte(`{"type":"response.output_item.done","output_index":0,"item":{"id":"answer","type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Child result."}]}}`))
					if err != nil {
						t.Fatal(err)
					}
					output = bytes.Join(events, nil)
					events, err = child.TransformSSE(append(append([]byte(`{"type":"response.completed","response":`), response...), '}'))
					if err != nil {
						t.Fatal(err)
					}
					output = append(output, bytes.Join(events, nil)...)
				} else {
					var err error
					output, err = child.TransformJSON(response)
					if err != nil {
						t.Fatal(err)
					}
				}
				if bytes.Contains(output, []byte("Router session usage")) || !bytes.Contains(output, []byte("Child result.")) {
					t.Fatalf("child emitted usage or lost answer: %s", output)
				}
				child.Close()
				root, _ = prepareActivityTest(t, proxy, "remapped", "root", "", "/root", nil)
				root.usageTracker.model = "gpt-6-astra"
				root.observeResponseUsage(tokenCounts{InputTokens: 100, UncachedInputTokens: 50, OutputTokens: 10, ReasoningTokens: 5})
				if stream {
					events, err := root.TransformSSE([]byte(`{"type":"response.created","response":{"id":"root-response"}}`))
					if err != nil {
						t.Fatal(err)
					}
					if bytes.Contains(bytes.Join(events, nil), []byte("Router session usage")) {
						t.Fatal("usage appeared before root completion")
					}
				}
				rootResponse := []byte(`{"id":"root-final","status":"completed","output":[{"id":"root-answer","type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Root answer."}]}]}`)
				if stream {
					_, err := root.TransformSSE([]byte(`{"type":"response.output_item.done","output_index":0,"item":{"id":"root-answer","type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Root answer."}]}}`))
					if err != nil {
						t.Fatal(err)
					}
					events, err := root.TransformSSE(append(append([]byte(`{"type":"response.completed","response":`), rootResponse...), '}'))
					if err != nil {
						t.Fatal(err)
					}
					output = bytes.Join(events, nil)
				} else {
					var err error
					output, err = root.TransformJSON(rootResponse)
					if err != nil {
						t.Fatal(err)
					}
				}
				if bytes.Contains(output, []byte("Router session usage")) ||
					!bytes.Contains(output, []byte("Journal flush")) || !bytes.Contains(output, []byte("Root answer.")) ||
					bytes.Contains(output, []byte(`"id":"root-answer"`)) {
					t.Fatalf("main completion emitted usage commentary or lost the journal answer: %s", output)
				}
				root.ReleaseDelivery()
			})
		}
	}
}
