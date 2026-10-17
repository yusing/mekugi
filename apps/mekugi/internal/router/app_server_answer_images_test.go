package router

import (
	"bytes"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	json "encoding/json/v2"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"

	conversationbinding "github.com/yusing/mekugi/modules/conversation/codexbinding"
)

func answerImageFixture(t *testing.T) string {
	t.Helper()
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "answer image.png")
	if err := os.WriteFile(path, data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestQuestionAsyncImagesUseComposerInput(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.startSession("main", t.TempDir())
	u.Turn = "turn"
	appServerTestKeys(t, u, "main draft")
	questionTestAsync(t, u, "photos", "First image?", "Second image?")
	u.openQuestions()
	questionTestPaint(t, u, 70)
	first, second := answerImageFixture(t), answerImageFixture(t)
	appServerTestKeys(t, u, "\x1b[200~"+first+"\x1b[201~")
	if u.Text != "[Image 1] " || len(u.Images) != 1 || len(sessionpresentation.DraftSpans(u.draftSnapshot().Draft)) != 1 {
		t.Fatalf("answer did not reuse composer: %q %+v", u.Text, u.Images)
	}
	u.insertSelection("message", "first quote", "")
	appServerTestKeys(t, u, "\r")
	questionTestPaint(t, u, 70)
	appServerTestKeys(t, u, "\x1b[200~"+second+"\x1b[201~")
	u.insertSelection("message", "second quote", "")
	appServerTestKeys(t, u, "\r")
	if u.Text != "main draft" {
		t.Fatalf("parked editor changed: %q", u.Text)
	}
	var request struct {
		Method string `json:"method"`
		Params struct {
			Input []struct {
				Type string `json:"type"`
				Text string `json:"text"`
				Path string `json:"path"`
			} `json:"input"`
		} `json:"params"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(wire.Bytes()), &request); err != nil {
		t.Fatal(err)
	}
	if request.Method != "turn/steer" || len(request.Params.Input) != 4 {
		t.Fatalf("wrong answer input: %+v", request)
	}
	input := request.Params.Input
	replies := binding.QuestionReplies(input[0].Text)
	if len(replies) != 2 || replies[0].Answer != "[Image 1] [Selected message] " || replies[1].Answer != "[Image 2] [Selected message 2] " {
		t.Fatalf("wrong image identity: %+v", replies)
	}
	if input[1].Type != "localImage" || input[1].Path != first || input[2].Type != "localImage" || input[2].Path != second {
		t.Fatalf("images became paths: %+v", input)
	}
	frames, ok := attachments.Decode(input[3].Text)
	if !ok || len(frames) != 2 || !strings.HasSuffix(frames[0], "\nfirst quote") || !strings.HasSuffix(frames[1], "\nsecond quote") {
		t.Fatalf("answer selections lost: %q", frames)
	}
	content, err := json.Marshal(conversationbinding.Input(u.attached.conversation.Submitted[u.Submission().ID].Draft))
	if err != nil {
		t.Fatal(err)
	}
	text, spans, ok := sessionpresentation.UserText(content)
	if !ok || len(binding.QuestionReplies(text)) != 2 || len(spans) != 0 {
		t.Fatalf("reply images created a user band: %q %+v", text, spans)
	}
	questionTestMessage(t, u, nil, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": map[string]any{"id": "reply", "type": "userMessage", "clientUserMessageId": u.Submission().ID, "content": conversationbinding.Input(u.attached.conversation.Submitted[u.Submission().ID].Draft)}})
	for _, q := range u.questionCalls()[0].Questions {
		if q.Outcome != "answered" {
			t.Fatalf("image answer not committed: %+v", q)
		}
	}
	if _, err := os.Stat(first); err != nil {
		t.Fatal("user image removed", err)
	}
}

func TestQuestionSyncImageSendsHostOwnedCompanion(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.Turn = "turn"
	questionTestSync(t, u, "image-sync", false)
	questionTestPaint(t, u, 70)
	path := answerImageFixture(t)
	appServerTestKeys(t, u, "\x1b[200~"+path+"\x1b[201~\r")
	if !strings.Contains(wire.String(), `"user_note: [Image 1] "`) || strings.Contains(wire.String(), `"localImage"`) {
		t.Fatalf("sync tool response must be strings, companion waits for resolution: %s", wire.String())
	}
	if len(u.attached.conversation.Unsent) != 1 || len(u.attached.conversation.Unsent[0].Images) != 1 {
		t.Fatal("missing image companion")
	}
	wire.Reset()
	questionTestMessage(t, u, nil, "serverRequest/resolved", map[string]any{"threadId": "main", "requestId": "image-sync"})
	if !strings.Contains(wire.String(), `"type":"localImage"`) || !strings.Contains(wire.String(), path) {
		t.Fatalf("companion did not use composer serialization: %s", wire.String())
	}
}

func TestQuestionImageLifetimeAcrossNavigationAndRetry(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.startSession("main", t.TempDir())
	u.Turn = "turn"
	questionTestAsync(t, u, "retained", "First?", "Second?")
	questionTestPaint(t, u, 70)
	path := answerImageFixture(t)
	u.attachImage(path)
	u.insertSelection("message", "retry quote", "")
	appServerTestKeys(t, u, "\r")
	u.pruneDraftImages()
	if _, err := os.Stat(path); err != nil {
		t.Fatal("parked answer image reclaimed", err)
	}
	questionTestPaint(t, u, 70)
	appServerTestKeys(t, u, "1\r")
	request := resumeTestOne(t, wire, "turn/steer")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"rejected"}}`, request.ID))
	u.openQuestions()
	questionTestPaint(t, u, 70)
	if len(u.Images) != 1 || u.Images[0].Path != path || len(u.Selections) != 1 || u.Selections[0].Text != "retry quote" {
		t.Fatalf("retry lost image: %q %+v", u.Text, u.Images)
	}
	wire.Reset()
	appServerTestKeys(t, u, "\r")
	if !strings.Contains(wire.String(), `"type":"localImage"`) || !strings.Contains(wire.String(), "retry quote") {
		t.Fatalf("retry omitted image: %s", wire.String())
	}
}

func TestQuestionSkippedImageIsReclaimed(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.Turn = "turn"
	questionTestAsync(t, u, "skip", "Image?")
	questionTestPaint(t, u, 70)
	path := answerImageFixture(t)
	u.attachImage(path)
	appServerTestKeys(t, u, "\x1d\r")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("skipped generated image retained: %v", err)
	}
}

func TestQuestionSecretDraftKeepsImageWithoutExposingModelState(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.Turn = "turn"
	questionTestSync(t, u, "secret-image", true)
	questionTestPaint(t, u, 70)
	path := answerImageFixture(t)
	u.attachImage(path)
	u.hideQuestions()
	u.pruneDraftImages()
	if _, err := os.Stat(path); err != nil {
		t.Fatal("parked secret image reclaimed", err)
	}
	call := u.questionCalls()[0]
	if call.Questions[0].Editor.Snapshot.Text != "" || len(call.Questions[0].Answer) != 0 || len(call.View().Questions[0].Answer) != 0 {
		t.Fatal("secret draft exposed through model")
	}
	u.openQuestions()
	questionTestPaint(t, u, 70)
	appServerTestKeys(t, u, "\r")
	if !strings.Contains(wire.String(), "user_note: [Image 1]") || len(u.attached.conversation.Unsent) != 1 || len(u.attached.conversation.Unsent[0].Images) != 1 {
		t.Fatal("secret answer image companion lost")
	}
	if len(u.shell.Prompts.SecretState(call.ID).Editors) != 0 || len(u.shell.Prompts.SecretState(call.ID).Answers) != 0 || len(call.Questions[0].Answer) != 0 {
		t.Fatal("submitted secret retained")
	}
}
