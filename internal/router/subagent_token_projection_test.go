package router

import (
	"bytes"
	"strings"
	"testing"
)

func TestChildTokenUsageProjectsToRoot(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, missing := range []bool{false, true} {
			t.Run(map[bool]string{false: "json", true: "sse"}[stream]+map[bool]string{false: "/complete", true: "/missing"}[missing], func(t *testing.T) {
				proxy := newManagedMekugiProxy(t)
				root, _ := prepareCommentaryActivityTest(t, proxy, "shared-session", "root", "", "/root", nil)
				other, _ := prepareCommentaryActivityTest(t, proxy, "other-session", "other", "", "/root", nil)
				child, _ := prepareCommentaryActivityTest(t, proxy, "shared-session", "child", "root", "/root/worker", nil)
				child.usageTracker.model = "gpt-5.6-sol"
				root.drainActivity()
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
				if bytes.Contains(output, []byte("Tokens for this session")) || !bytes.Contains(output, []byte("Child result.")) {
					t.Fatalf("child emitted usage or lost answer: %s", output)
				}
				child.Close()
				root, _ = prepareCommentaryActivityTest(t, proxy, "remapped", "root", "", "/root", nil)
				root.usageTracker.model = "gpt-6-astra"
				root.observeResponseUsage(tokenCounts{InputTokens: 100, UncachedInputTokens: 50, OutputTokens: 10, ReasoningTokens: 5})
				if stream {
					events, err := root.TransformSSE([]byte(`{"type":"response.created","response":{"id":"root-response"}}`))
					if err != nil {
						t.Fatal(err)
					}
					if bytes.Contains(bytes.Join(events, nil), []byte("Tokens for this session")) {
						t.Fatal("usage appeared before root completion")
					}
				}
				report, ok := root.completionUsageReport()
				text := formatTokenUsageReport(report)
				if !ok || strings.Count(text, "| Agent |") != 1 || !strings.Contains(text, "| /root/worker | n/a |") {
					t.Fatalf("missing consolidated table: %s", text)
				}
				if missing {
					if !report.Incomplete || !strings.Contains(text, "| Total | — | — | n/a |") {
						t.Fatal(text)
					}
				} else if report.InputTokens != 220 || report.OutputTokens != 40 || !strings.Contains(text, "| /root/worker | n/a | gpt-5.6-sol | 120 (66.7%) |") {
					t.Fatal(text)
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
				if bytes.Count(output, []byte("Tokens for this session")) != 1 || !bytes.Contains(output, []byte("/root/worker")) || !bytes.Contains(output, []byte("Root answer.")) {
					t.Fatalf("main completion did not deliver one consolidated report: %s", output)
				}
				other.observeResponseUsage(tokenCounts{InputTokens: 1})
				otherReport, _ := other.completionUsageReport()
				if strings.Contains(formatTokenUsageReport(otherReport), "/root/worker") {
					t.Fatal("child reached unrelated root")
				}
				root.ReleaseDelivery()
			})
		}
	}
}
