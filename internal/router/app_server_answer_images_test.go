package router

import (
	"bytes"
	json "encoding/json/v2"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	u.session.start("main", t.TempDir())
	u.turn = "turn"
	appServerTestKeys(t, u, "main draft")
	questionTestAsync(t, u, "photos", "First image?", "Second image?")
	u.openQuestions()
	questionTestPaint(t, u, 70)
	first, second := answerImageFixture(t), answerImageFixture(t)
	appServerTestKeys(t, u, "\x1b[200~"+first+"\x1b[201~")
	if u.draft != "[Image 1] " || len(u.images) != 1 || len(u.draftSnapshot().displaySpans()) != 1 {
		t.Fatalf("answer did not reuse composer: %q %+v", u.draft, u.images)
	}
	appServerTestKeys(t, u, "\r")
	questionTestPaint(t, u, 70)
	appServerTestKeys(t, u, "\x1b[200~"+second+"\x1b[201~\r")
	if u.draft != "main draft" {
		t.Fatalf("parked editor changed: %q", u.draft)
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
	if request.Method != "turn/steer" || len(request.Params.Input) != 3 {
		t.Fatalf("wrong answer input: %+v", request)
	}
	input := request.Params.Input
	replies := questionReplies(input[0].Text)
	if len(replies) != 2 || replies[0].Answer != "[Image 1] " || replies[1].Answer != "[Image 2] " {
		t.Fatalf("wrong image identity: %+v", replies)
	}
	if input[1].Type != "localImage" || input[1].Path != first || input[2].Type != "localImage" || input[2].Path != second {
		t.Fatalf("images became paths: %+v", input)
	}
	content, err := json.Marshal(u.submission.input())
	if err != nil {
		t.Fatal(err)
	}
	text, spans, ok := appServerUserText(content)
	if !ok || len(questionReplies(text)) != 2 || len(spans) != 0 {
		t.Fatalf("reply images created a user band: %q %+v", text, spans)
	}
	questionTestMessage(t, u, nil, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": map[string]any{"id": "reply", "type": "userMessage", "clientUserMessageId": u.submission.id, "content": u.submission.input()}})
	for _, q := range u.questions.calls[0].questions {
		if q.outcome != "answered" {
			t.Fatalf("image answer not committed: %+v", q)
		}
	}
	if _, err := os.Stat(first); err != nil {
		t.Fatal("user image removed", err)
	}
}

func TestQuestionSyncImageSendsHostOwnedCompanion(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.turn = "turn"
	questionTestSync(t, u, "image-sync", false)
	questionTestPaint(t, u, 70)
	path := answerImageFixture(t)
	appServerTestKeys(t, u, "\x1b[200~"+path+"\x1b[201~\r")
	if !strings.Contains(wire.String(), `"user_note: [Image 1] "`) || strings.Contains(wire.String(), `"localImage"`) {
		t.Fatalf("sync tool response must be strings, companion waits for resolution: %s", wire.String())
	}
	if len(u.unsent) != 1 || len(u.unsent[0].images) != 1 {
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
	u.session.start("main", t.TempDir())
	u.turn = "turn"
	questionTestAsync(t, u, "retained", "First?", "Second?")
	questionTestPaint(t, u, 70)
	path := answerImageFixture(t)
	u.attachImage(path)
	appServerTestKeys(t, u, "\r")
	u.pruneDraftImages()
	if _, err := os.Stat(path); err != nil {
		t.Fatal("parked answer image reclaimed", err)
	}
	questionTestPaint(t, u, 70)
	appServerTestKeys(t, u, "1\r")
	requestID := ""
	for id, method := range u.requests {
		if method == "turn/steer" {
			requestID = id
		}
	}
	appServerTestMessage(t, u, `{"id":`+requestID+`,"error":{"code":-1,"message":"rejected"}}`)
	u.openQuestions()
	questionTestPaint(t, u, 70)
	if len(u.images) != 1 || u.images[0].path != path {
		t.Fatalf("retry lost image: %q %+v", u.draft, u.images)
	}
	wire.Reset()
	appServerTestKeys(t, u, "\r")
	if !strings.Contains(wire.String(), `"type":"localImage"`) {
		t.Fatalf("retry omitted image: %s", wire.String())
	}
}

func TestQuestionSkippedImageIsReclaimed(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.turn = "turn"
	questionTestAsync(t, u, "skip", "Image?")
	questionTestPaint(t, u, 70)
	path := answerImageFixture(t)
	u.attachImage(path)
	appServerTestKeys(t, u, "\x1d\r")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("skipped generated image retained: %v", err)
	}
}
