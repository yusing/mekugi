package router

import (
	json "encoding/json/v2"
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

func TestAppServerTypedTokensAcrossSubmissionAndResume(t *testing.T) {
	u, _ := newAppServerTestUI()
	appServerTestKeys(t, u, "世界 literal $review @file ")
	u.insertImage("/tmp/image.png")
	appServerTestKeys(t, u, " ")
	bindComposerFile(u, `@"file name.go"`, "/work/file name.go")
	appServerTestKeys(t, u, " ")
	start := len(u.draft)
	appServerTestKeys(t, u, "$review")
	u.skills = append(u.skills, composerSkill{start: start, end: len(u.draft), name: "review", path: "/work/review/SKILL.md"})
	draft := u.draftSnapshot()
	content, err := json.Marshal(draft.input())
	if err != nil {
		t.Fatal(err)
	}
	text, spans, ok := appServerUserText(content)
	if !ok || text != draft.text || len(spans) != 3 {
		t.Fatalf("lost typed spans or bound literal lookalike: %q %+v", text, spans)
	}
	if spans[0].Kind != activityui.ImageToken || spans[1].Kind != activityui.FileToken || spans[2].Kind != activityui.SkillToken {
		t.Fatalf("lost token kinds: %+v", spans)
	}
	check := func(v *liveActivityView) {
		t.Helper()
		entry := v.entries[0]
		if !reflect.DeepEqual(entry.native.spans, spans) {
			t.Fatalf("wrong spans: %+v", entry.native.spans)
		}
		var out conversationLines
		v.userItemContinued(&out, entry, 24, false)
		shown := ansi.Strip(strings.Join(out.lines, "\n"))
		for _, span := range spans {
			if !strings.Contains(shown, text[span.Start:span.End]) {
				t.Fatalf("split token: %q", shown)
			}
		}
	}
	appServerTestKeys(t, u, "\r")
	check(u.view)
	item := appServerItem{ID: "user", Type: "userMessage", Content: content}
	for _, method := range []string{"item/started", "item/completed"} {
		appServerTestNotify(t, u, method, map[string]any{"threadId": "main", "turnId": "t", "item": item})
		check(u.view)
	}
	restored := newAppServerSessionTestUI(t, t.TempDir())
	restored.restoreHistory([]appServerHistoryTurn{{ID: "t", Status: "completed", Items: []appServerItem{item}}})
	check(restored.view)
}

func TestComposerElementSpansRejectInvalidRanges(t *testing.T) {
	text := "界@file"
	elements := []composerTextElement{
		{composerByteRange{-1, 4}, "bad"},
		{composerByteRange{1, 6}, text[1:6]},
		{composerByteRange{3, 100}, "@file"},
		{composerByteRange{3, 8}, "@file"},
		{composerByteRange{3, 8}, "@file"},
	}
	spans := composerElementSpans(text, elements)
	if len(spans) != 1 || spans[0].Start != 3 || spans[0].End != 8 {
		t.Fatalf("invalid spans: %+v", spans)
	}
}
