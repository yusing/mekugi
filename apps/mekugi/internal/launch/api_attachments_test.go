package launch

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

func TestAPISelectionReferenceSubmission(t *testing.T) {
	workspace := t.TempDir()
	quote := "A selected answer with Unicode: 日本語"
	selection := sessionapi.Attachment{Source: "selection", Name: "Selected output", Path: "source.go", Data: new(base64.StdEncoding.EncodeToString([]byte(quote)))}
	inline := selection
	inline.Inline = true
	text := "日本語 Why [Selected output] now?"
	prepared, err := prepareAPIInput(t.Context(), workspace, sessionapi.InputRequest{Text: text, Attachments: []sessionapi.Attachment{inline, selection}})
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Cleanup()
	if len(prepared.Draft.Selections) != 2 || len(prepared.Draft.Files) != 0 || !strings.Contains(prepared.Draft.Text, "[Selected output 2]") {
		t.Fatalf("reference bindings: %+v", prepared.Draft)
	}
	if !strings.HasPrefix(prepared.Draft.Text, text+"\n") || prepared.Draft.Selections[0].Start != len("日本語 Why ") {
		t.Fatalf("inline reference moved: %+v", prepared.Draft)
	}
	u, wire := newAppServerTestUI()
	u.startSession("main", workspace)
	if refusal := apiTestIntent(t, u, sessionapi.Intent{Action: sessionapi.Send, Prepared: prepared, Thread: "main", Workspace: workspace, Context: t.Context()}); refusal != "" {
		t.Fatal(refusal)
	}
	requests := appServerTurnRequests(t, wire)
	if text := requests[0].text(); !strings.HasPrefix(text, prepared.Draft.Text) || strings.Count(text, quote) != 2 || !strings.Contains(text, "User selections:") {
		t.Fatalf("submitted selection text: %q", text)
	}
	selection.Data = new(base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", attachments.Budget))))
	if _, err := prepareAPIInput(t.Context(), workspace, sessionapi.InputRequest{Attachments: []sessionapi.Attachment{selection}}); err == nil {
		t.Fatal("oversized selection admitted")
	}
	for _, invalid := range []sessionapi.InputRequest{
		{Text: "marker removed", Attachments: []sessionapi.Attachment{inline}},
		{Text: text + text, Attachments: []sessionapi.Attachment{inline}},
		{Text: text, Attachments: []sessionapi.Attachment{inline, inline}},
		{Text: text, Attachments: []sessionapi.Attachment{{Source: "path", Path: "source.go", Inline: true}}},
	} {
		if _, err := prepareAPIInput(t.Context(), workspace, invalid); err == nil {
			t.Fatalf("invalid inline attachment admitted: %+v", invalid)
		}
	}
}

func TestAPIUserSelectionsReachAppServerAsPlainMarkdown(t *testing.T) {
	workspace := t.TempDir()
	prepared, err := prepareAPIInput(t.Context(), workspace, sessionapi.InputRequest{Text: "Explain [Selected diff]", Attachments: []sessionapi.Attachment{
		{Source: "selection", Name: "diff", Path: "sample.rs", Data: new(base64.StdEncoding.EncodeToString([]byte("before\nafter"))), Inline: true},
		{Source: "upload", Name: "notes.txt", Data: new(base64.StdEncoding.EncodeToString([]byte("uploaded notes")))},
		{Source: "path", Path: "missing source.rs"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Cleanup()
	u, wire := newAppServerTestUI()
	u.startSession("main", workspace)
	if refusal := apiTestIntent(t, u, sessionapi.Intent{Action: sessionapi.Send, Prepared: prepared, Context: t.Context(), Thread: "main", Workspace: workspace}); refusal != "" {
		t.Fatal(refusal)
	}
	requests := appServerTurnRequests(t, wire)
	if len(requests) != 1 {
		t.Fatalf("native requests: %+v", requests)
	}
	text := requests[0].text()
	for _, expected := range []string{"Explain [Selected diff]", `@'missing source.rs'`, "\n---\nUser selections:\n", "- diff:\n  ```rust\n  before\n  after\n  ```", "- Uploaded notes.txt: uploaded notes"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("missing %q in native user input: %q", expected, text)
		}
	}
	if strings.Contains(text, "mekugi-file-attachments") || strings.Contains(text, "CONTENT NOT ATTACHED") {
		t.Fatalf("Go emitted a file envelope or acquired a host source: %q", text)
	}
}

func TestAPIExpandedSelectionTextLimitPreservesInput(t *testing.T) {
	const label = "[Selected message]"
	draft := composer.Draft{Text: label, Selections: []composer.Token{{Start: 0, End: len(label), Text: strings.Repeat("x", responses.UserTextLimit)}}}
	if prepared, err := prepareNativeDraft(t.Context(), t.TempDir(), draft); prepared != nil || err == nil {
		if prepared != nil {
			prepared.Cleanup()
		}
		t.Fatalf("expanded text exceeded the host limit without refusal: %v", err)
	} else if failure, ok := errors.AsType[*sessionapi.InputError](err); !ok || !failure.Limit {
		t.Fatalf("wrong limit classification: %v", err)
	}
	if len(draft.Selections) != 1 || draft.Selections[0].Text != strings.Repeat("x", responses.UserTextLimit) {
		t.Fatal("limit rejection consumed supplied text")
	}
	draft.Selections[0].Text = "nearby valid selection"
	prepared, err := prepareNativeDraft(t.Context(), t.TempDir(), draft)
	if err != nil {
		t.Fatal(err)
	}
	prepared.Cleanup()
}

func TestAPIAttachmentCancellationAndPermanentErrors(t *testing.T) {
	workspace := t.TempDir()
	borrowed := pasteTestImage(t)
	data, err := os.ReadFile(borrowed)
	if err != nil {
		t.Fatal(err)
	}
	image := sessionapi.Attachment{Source: "upload", Name: "image", Data: new(base64.StdEncoding.EncodeToString(data))}
	prepared, err := prepareAPIInput(t.Context(), workspace, sessionapi.InputRequest{Attachments: []sessionapi.Attachment{{Source: "path", Path: borrowed}, image}})
	if err != nil {
		t.Fatal(err)
	}
	acquired := prepared.Draft.Images[1].Path
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	u, wire := newAppServerTestUI()
	u.startSession("main", workspace)
	for _, intent := range []sessionapi.Intent{
		{Action: sessionapi.Send, Prepared: prepared, Context: ctx, Thread: "main", Workspace: workspace},
		{Action: sessionapi.Send, Prepared: prepared, Context: t.Context(), Thread: "stale", Workspace: workspace},
	} {
		if refusal := apiTestIntent(t, u, intent); refusal == "" || wire.Len() != 0 {
			t.Fatal("canceled/stale input reached host")
		}
	}
	prepared.Cleanup()
	if _, err := os.Stat(acquired); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("canceled acquired image survived", err)
	}
	if _, err := os.Stat(borrowed); err != nil {
		t.Fatal("borrowed image was removed", err)
	}
	for _, attachment := range []sessionapi.Attachment{
		{Source: "invalid"}, {Source: "upload", Name: "missing data"}, {Source: "upload", Name: "bad", Data: new("not base64")}, {Source: "path"},
	} {
		if _, err := prepareAPIInput(t.Context(), workspace, sessionapi.InputRequest{Attachments: []sessionapi.Attachment{attachment}}); err == nil {
			t.Fatalf("malformed attachment accepted: %+v", attachment)
		} else if _, ok := errors.AsType[*sessionapi.InputError](err); !ok {
			t.Fatalf("error lacks permanent classification: %v", err)
		}
	}
	if _, err := prepareAPIInput(t.Context(), workspace, sessionapi.InputRequest{Attachments: []sessionapi.Attachment{{Source: "upload", Name: "binary", Data: new(base64.StdEncoding.EncodeToString([]byte{0}))}}}); err == nil {
		t.Fatal("binary content accepted as text")
	}
	// Failure after acquisition releases the newly written file, never the
	// preceding borrowed path or a caller-selected upload name.
	temporary := t.TempDir()
	t.Setenv("TMPDIR", temporary)
	_, err = prepareAPIInput(t.Context(), workspace, sessionapi.InputRequest{Attachments: []sessionapi.Attachment{image, {Source: "invalid"}}})
	if err == nil {
		t.Fatal("invalid tail accepted")
	}
	entries, err := os.ReadDir(temporary)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed preparation leaked acquired images: %v %v", entries, err)
	}
}

func TestAPIAttachmentCombinedUploadLimit(t *testing.T) {
	workspace := t.TempDir()
	data, err := os.ReadFile(pasteTestImage(t))
	if err != nil {
		t.Fatal(err)
	}
	chunk := base64.StdEncoding.EncodeToString(append(data, make([]byte, sessionapi.UploadLimit/2-len(data))...))
	input := sessionapi.InputRequest{Attachments: []sessionapi.Attachment{{Source: "upload", Name: "one", Data: new(chunk)}, {Source: "upload", Name: "two", Data: new(chunk)}}}
	prepared, err := prepareAPIInput(t.Context(), workspace, input)
	if err != nil {
		t.Fatalf("exact combined upload limit rejected: %v", err)
	}
	prepared.Cleanup()
	input.Attachments = append(input.Attachments, sessionapi.Attachment{Source: "upload", Name: "extra", Data: new("eA==")})
	_, err = prepareAPIInput(t.Context(), workspace, input)
	failure, ok := errors.AsType[*sessionapi.InputError](err)
	if !ok || !failure.Limit {
		t.Fatalf("combined decoded overflow not classified: %v", err)
	}
}
