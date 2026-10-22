package launch

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
)

func TestAPIAttachmentSnapshotAdmissionAndContinuation(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "host.txt")
	if err := os.WriteFile(path, []byte("captured host contents"), 0600); err != nil {
		t.Fatal(err)
	}
	borrowed := pasteTestImage(t)
	image, err := os.ReadFile(borrowed)
	if err != nil {
		t.Fatal(err)
	}
	request := sessionapi.InputRequest{Attachments: []sessionapi.Attachment{
		{Source: "upload", Name: "../../label.png", Data: new(base64.StdEncoding.EncodeToString([]byte("uploaded text, not an image")))},
		{Source: "path", Path: "host.txt"},
		{Source: "upload", Name: "picture", Data: new(base64.StdEncoding.EncodeToString(image))},
		{Source: "path", Path: borrowed},
		{Source: "path", Path: "."},
	}}
	prepared, err := prepareAPIInput(t.Context(), workspace, request)
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Cleanup()
	acquired := prepared.Draft.Images[0].Path
	t.Cleanup(func() { _ = os.Remove(acquired) }) // A submitted image is deliberately retained.
	if len(prepared.Draft.Images) != 2 || len(prepared.Draft.Files) != 3 || prepared.Draft.Images[1].Path != borrowed {
		t.Fatalf("attachment classification: %+v", prepared.Draft)
	}
	if err := os.WriteFile(path, []byte("changed source"), 0600); err != nil {
		t.Fatal(err)
	}
	u, wire := newAppServerTestUI()
	u.startSession("main", workspace)
	intent := sessionapi.Intent{Action: sessionapi.Send, Prepared: prepared, Thread: "main", Workspace: workspace, Context: t.Context()}
	u.attached.conversation.Approvals.Open = true
	if refusal := apiTestIntent(t, u, intent); refusal == "" || prepared.Admitted() {
		t.Fatal("open prompt admitted an attachment")
	}
	if _, err := os.Stat(acquired); err != nil {
		t.Fatal("retry lost its acquired image", err)
	}
	u.attached.conversation.Approvals.Open = false
	if refusal := apiTestIntent(t, u, intent); refusal != "" {
		t.Fatal(refusal)
	}
	requests := appServerTurnRequests(t, wire)
	if len(requests) != 1 || requests[0].Method != "turn/start" {
		t.Fatalf("first input: %+v", requests)
	}
	var frames []string
	var images []string
	for _, part := range requests[0].Params.Input {
		if part.Path != "" {
			images = append(images, part.Path)
		}
		if decoded, ok := attachments.Decode(part.Text); ok {
			frames = append(frames, decoded...)
		}
	}
	if len(images) != 2 || images[0] != acquired || images[1] != borrowed || len(frames) != 3 || !strings.Contains(frames[0], "uploaded text, not an image") || !strings.Contains(frames[1], "captured host contents") || !strings.Contains(frames[2], "directory tree") {
		t.Fatalf("snapshot order/content: images=%v frames=%q", images, frames)
	}
	prepared.Cleanup()
	if !prepared.Admitted() || u.ownedImages[acquired] {
		t.Fatal("submitted image did not transfer to existing continuation retention")
	}
	for _, imagePath := range images {
		if _, err := os.Stat(imagePath); err != nil {
			t.Fatal("continuation image was removed", err)
		}
	}
	// Later input uses the same preparation and admission path while Main runs.
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"turn":{"id":"turn"}}}`, requests[0].ID))
	appServerTestTurn(t, u, "turn")
	later, err := prepareAPIInput(t.Context(), workspace, sessionapi.InputRequest{Attachments: []sessionapi.Attachment{{Source: "upload", Name: "later.txt", Data: new(base64.StdEncoding.EncodeToString([]byte("later snapshot")))}}})
	if err != nil {
		t.Fatal(err)
	}
	defer later.Cleanup()
	if refusal := apiTestIntent(t, u, sessionapi.Intent{Action: sessionapi.Send, Prepared: later, Context: t.Context(), Thread: "main", Workspace: workspace}); refusal != "" {
		t.Fatal(refusal)
	}
	requests = appServerTurnRequests(t, wire)
	if len(requests) != 1 || requests[0].Method != "turn/steer" || !strings.Contains(requests[0].text(), "later snapshot") {
		t.Fatalf("later input: %+v", requests)
	}
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
	omitted, err := prepareAPIInput(t.Context(), workspace, sessionapi.InputRequest{Attachments: []sessionapi.Attachment{{Source: "upload", Name: "binary", Data: new(base64.StdEncoding.EncodeToString([]byte{0}))}}})
	if err != nil || omitted.Draft.AttachmentNotice == "" || !strings.Contains(omitted.Draft.Attachments[0], "CONTENT NOT ATTACHED") {
		t.Fatalf("existing omission semantics lost: prepared=%+v err=%v", omitted, err)
	}
	omitted.Cleanup()
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
	chunk := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", sessionapi.UploadLimit/2)))
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
