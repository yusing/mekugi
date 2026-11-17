package executionobservation

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
	conversationbinding "github.com/yusing/mekugi/modules/conversation/codexbinding"
	"github.com/yusing/mekugi/modules/conversation/composer"
	"github.com/yusing/mekugi/testsupport/moduletest"
)

func attachmentTestContext(values []composer.ContextAttachment) func(context.Context, string, string) (composer.AttachmentSnapshot, error) {
	var cache composer.AttachmentContext
	return func(_ context.Context, _, window string) (composer.AttachmentSnapshot, error) {
		if snapshot, ok := cache.Get(window); ok {
			return snapshot, nil
		}
		cache.Set(window, values)
		snapshot, _ := cache.Get(window)
		return snapshot, nil
	}
}

func attachmentTestReadHints(t *testing.T, p *Preparer, thread string, input []byte) string {
	t.Helper()
	matches := regexp.MustCompile(`mread ([a-z]+[0-9]*(?: [a-z]+[0-9]*)*)`).FindAllStringSubmatch(attachmentTestHint(t, input), -1)
	namespace, err := p.Retained.Store.NamespaceForThread(thread)
	if err != nil {
		t.Fatal(err)
	}
	var args []string
	for _, match := range matches {
		args = append(args, strings.Fields(match[1])...)
	}
	if len(args) == 0 {
		return ""
	}
	out, err := p.Recovery.Execute(t.Context(), command.Request{Args: args, Scope: scope.Retained{Directory: p.Retained.Store.Directory(), Namespace: namespace, Thread: thread}})
	if err != nil || out.ExitCode != 0 {
		t.Fatalf("mread: %+v, %v", out, err)
	}
	return out.Stdout
}

func TestPlainInputAttachmentRecallThroughNativeHistory(t *testing.T) {
	for _, mode := range []string{"ordinary", "async answer", "sync companion"} {
		t.Run(mode, func(t *testing.T) {
			const original = "Review @missing $review [Selected message] [Uploaded notes.txt]"
			token := func(label string) composer.Token {
				start := strings.Index(original, label)
				return composer.Token{Start: start, End: start + len(label)}
			}
			file, skill := token("@missing"), token("$review")
			file.Path, skill.Name = "missing", "review"
			selection := token("[Selected message]")
			selection.Text = "original quote 日本語"
			upload := token("[Uploaded notes.txt]")
			upload.Name, upload.Source, upload.Text = "notes.txt", "upload", "supplied upload"
			draft := composer.Draft{Text: original, Files: []composer.Token{file, upload}, Skills: []composer.Token{skill}, Selections: []composer.Token{selection}}
			if mode != "ordinary" {
				text := "Attachments accompanying answers to call:\n"
				if mode == "async answer" {
					text = binding.EncodeQuestionReplies([]binding.QuestionReply{{Answer: "[Selected message]", QuestionItemID: binding.AsyncQuestionIdentity("call", 0)}})
				}
				draft = composer.Draft{Text: text, AnswerSelections: draft.UserSelections()}
			}
			item := binding.ThreadItem{ID: "submitted", Type: "userMessage", Content: mustTestJSON(t, conversationbinding.Input(draft))}
			values := conversationbinding.CompactedAttachments([]binding.HistoryTurn{{Items: []binding.ThreadItem{item, {Type: "contextCompaction", Status: "completed"}}}})
			p := newManagedMekugiProxy(t)
			attachTestReplayStore(t, p)
			p.ContextAttachments = attachmentTestContext(values)
			request, err := codexresponses.ParseRequest(mustTestJSON(t, map[string]any{"input": []any{}, "tools": testExecResponsesTools()}))
			if err != nil {
				t.Fatal(err)
			}
			transform, err := p.prepareModelRequest(t.Context(), &request, "routing", "root", codexresponses.TurnMetadata{ThreadID: "root", RequestKind: "turn", ContextWindowID: "after-reset", WindowNumber: 1}, true, false)
			if err != nil {
				t.Fatal(err)
			}
			defer transform.Close()
			if bytes.Contains(request.Fields["input"], []byte("original quote")) || bytes.Contains(request.Fields["input"], []byte("supplied upload")) {
				t.Fatal("optional recall replayed supplied text automatically")
			}
			recalled := attachmentTestReadHints(t, p, "root", request.Fields["input"])
			for _, want := range []string{"User selections:", "message: original quote 日本語", "Uploaded notes.txt: supplied upload"} {
				if !strings.Contains(recalled, want) {
					t.Fatalf("missing %q in recalled input: %q", want, recalled)
				}
			}
			if mode == "ordinary" && (!strings.Contains(recalled, "@missing") || !strings.Contains(recalled, "$review") || !strings.Contains(recalled, "[Selected message]")) {
				t.Fatalf("native text-element placeholders lost: %q", recalled)
			}
		})
	}
}

func TestAttachmentContextPreservesSubmittedImagePath(t *testing.T) {
	for _, kind := range []string{"", "message", "diff"} {
		mixed := kind != ""
		for _, origin := range []string{"image", "answer"} {
			t.Run(fmt.Sprintf("%s/selection=%s", origin, kind), func(t *testing.T) {
				p := newManagedMekugiProxy(t)
				attachTestReplayStore(t, p)
				imagePath := filepath.Join(t.TempDir(), origin+"-image.png")
				var editor composer.Editor
				if mixed {
					editor.InsertText("[Selected " + kind + "]")
					body := "ORIGINAL_SELECTION\r\n日本語\n"
					if kind == "diff" {
						body = "+ORIGINAL_SELECTION\r\n-日本語\n"
					}
					editor.Selections = []composer.Token{{Start: 0, End: len(editor.Text), Text: body, Source: "screen"}}
				}
				if origin == "answer" {
					editor.InsertText(binding.EncodeQuestionReplies([]binding.QuestionReply{{Answer: "image answer", QuestionItemID: binding.AsyncQuestionIdentity("call", 0)}}))
					editor.AnswerImages = []string{imagePath}
				} else {
					editor.InsertImage(imagePath)
				}
				if mixed {
					editor.Attachments = []string{attachments.Encode(moduletest.SelectionFrames(editor.Text[editor.Selections[0].Start:editor.Selections[0].End], editor.Selections[0].Source, editor.Selections[0].Text))}
				}
				if err := moduletest.IdentifyAttachments(&editor.Attachments, "submitted"); err != nil {
					t.Fatal(err)
				}
				native := binding.ThreadItem{ID: "image-user", Type: "userMessage", Content: mustTestJSON(t, moduletest.WithLegacyAttachments(conversationbinding.Input(editor.Draft), editor.Draft.Attachments))}
				values := conversationbinding.CompactedAttachments([]binding.HistoryTurn{{Items: []binding.ThreadItem{native, {ID: "reset", Type: "contextCompaction", Status: "completed"}}}})
				p.ContextAttachments = attachmentTestContext(values)
				request, err := codexresponses.ParseRequest(mustTestJSON(t, map[string]any{"input": []any{}, "tools": testExecResponsesTools()}))
				if err != nil {
					t.Fatal(err)
				}
				transform, err := p.prepareModelRequest(t.Context(), &request, "routing", "root", codexresponses.TurnMetadata{ThreadID: "root", RequestKind: "turn", ContextWindowID: "new-window", WindowNumber: 1}, true, false)
				if err != nil {
					t.Fatal(err)
				}
				defer transform.Close()
				if len(transform.CompactReload()) != 0 || bytes.Contains(request.Fields["input"], []byte(imagePath)) || bytes.Contains(request.Fields["input"], []byte("ORIGINAL_SELECTION")) {
					t.Fatalf("attachment content forced into provider input: %s", request.Fields["input"])
				}
				pattern := `^Optional attachment recall: images: mread [a-z]+[0-9]*$`
				if mixed {
					pattern = `^Optional attachment recall: selections: mread [a-z]+[0-9]*; images: mread [a-z]+[0-9]*$`
				}
				if hint := attachmentTestHint(t, request.Fields["input"]); !regexp.MustCompile(pattern).MatchString(hint) {
					t.Fatalf("unexpected attachment hint: %q", hint)
				}
				recalled := attachmentTestReadHints(t, p, "root", request.Fields["input"])
				if !strings.Contains(recalled, imagePath) || !strings.Contains(recalled, "[Image 1]") || mixed && !strings.Contains(recalled, strings.Join(values[0].Selection.Frames, "\n")) {
					t.Fatalf("reset dropped submitted evidence: %s", recalled)
				}
				for _, noise := range []struct{ name, text string }{
					{"JSON", `"frames":`}, {"v2", "<mekugi-file-attachments-v2>"},
					{"id", `"id":`}, {"byte coordinates", "UTF-8 bytes"},
					{"file-attachments label", "file-attachments"},
				} {
					if strings.Contains(recalled, noise.text) {
						t.Fatalf("selection recall contains %s: %s", noise.name, recalled)
					}
				}
				if mixed {
					quote := strings.Join(values[0].Selection.Frames, "\n")
					selection, ok := attachments.ParseSelection(quote)
					if !ok || selection.Label != "[Selected "+kind+"]" || !strings.HasPrefix(selection.Header, "<selected-text ") || !strings.HasSuffix(selection.Footer, "</selected-text>") || (kind == "diff") != strings.HasSuffix(selection.Header, "```diff\n") {
						t.Fatalf("recall lost selection or diff framing: %q", quote)
					}
					at := strings.Index(recalled, quote)
					t.Logf("Recalled model-visible selection:\n%s", recalled[at:at+len(quote)])
				}
			})
		}
	}
}

func attachmentTestHint(t *testing.T, input []byte) string {
	t.Helper()
	var items []struct {
		Role    string `json:"role"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(input, &items); err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.Role != "user" {
			continue
		}
		for _, part := range item.Content {
			if strings.HasPrefix(part.Text, "Optional attachment recall:") {
				return part.Text
			}
		}
	}
	return ""
}
