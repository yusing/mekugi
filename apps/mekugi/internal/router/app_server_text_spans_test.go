package router

import (
	"context"
	json "encoding/json/v2"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	"github.com/yusing/mekugi/backends/codex/binding"
	conversationbinding "github.com/yusing/mekugi/modules/conversation/codexbinding"
	"github.com/yusing/mekugi/modules/conversation/composer"

	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"

	"github.com/charmbracelet/x/ansi"
)

func TestAppServerTypedTokensAcrossSubmissionAndResume(t *testing.T) {
	u, _ := newAppServerTestUI()
	skillPath := filepath.Join(t.TempDir(), "review", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skillPath, []byte("Review instruction 世界.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// This tests explicit attachment identity, not catalog discovery.
	u.picker.SkillsLoaded, u.picker.SkillsCwd = true, u.session.cwd
	appServerTestKeys(t, u, "世界 literal $review @file ")
	u.insertImage("/tmp/image.png")
	appServerTestKeys(t, u, " ")
	bindComposerFile(u, `@"file name.go"`, "/work/file name.go")
	appServerTestKeys(t, u, " ")
	start := len(u.Text)
	appServerTestKeys(t, u, "$review")
	u.Skills = append(u.Skills, composer.Token{Start: start, End: len(u.Text), Name: "review", Path: skillPath})
	draft := u.draftSnapshot()
	draft.SnapshotSkills(func(name, path string) ([]byte, error) {
		data, _, err := readSkillAttachment(context.Background(), u.session.cwd, nil, name, path)
		return data, err
	})
	input := conversationbinding.Input(draft.Draft)
	foundSkillContents := false
	for _, part := range input {
		if raw, ok := part["text"].(string); ok {
			frames, ok := attachments.Decode(raw)
			if ok && len(frames) == 1 && frames[0] == composer.SkillFrames("review", skillPath, "Review instruction 世界.\n")[0] {
				foundSkillContents = true
			}
		}
	}
	if !foundSkillContents {
		t.Fatalf("native skill source and contents missing from direct draft input: %+v", input)
	}
	content, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	text, spans, ok := appServerUserText(content)
	if !ok || text != draft.Text || len(spans) != 3 {
		t.Fatalf("lost typed spans or bound literal lookalike: %q %+v", text, spans)
	}
	if spans[0].Kind != activitypresentation.ImageToken || spans[1].Kind != activitypresentation.FileToken || spans[2].Kind != activitypresentation.SkillToken {
		t.Fatalf("lost token kinds: %+v", spans)
	}
	check := func(v *activityAdapter) {
		t.Helper()
		entry := v.Entries[0].Source
		if !reflect.DeepEqual(entry.native.spans, spans) {
			t.Fatalf("wrong spans: %+v", entry.native.spans)
		}
		out := v.testUserItem(entry, 24)
		shown := ansi.Strip(strings.Join(out.Lines, "\n"))
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
	elements := []binding.InputTextElement{
		{binding.InputByteRange{-1, 4}, "bad"},
		{binding.InputByteRange{1, 6}, text[1:6]},
		{binding.InputByteRange{3, 100}, "@file"},
		{binding.InputByteRange{3, 8}, "@file"},
		{binding.InputByteRange{3, 8}, "@file"},
	}
	spans := composerElementSpans(text, elements)
	if len(spans) != 1 || spans[0].Start != 3 || spans[0].End != 8 {
		t.Fatalf("invalid spans: %+v", spans)
	}
}
