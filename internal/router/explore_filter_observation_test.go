package router

import (
	"bytes"
	json "encoding/json/v2"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/tokenizer"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

func TestExploreFilterPaneEmission(t *testing.T) {
	for _, thread := range []string{"root", "probe"} {
		t.Run(thread, func(t *testing.T) {
			activity := newSubagentActivity()
			activity.attachNativePane("root")
			activity.observe("root", "", "/root", false)
			activity.observe("probe", "root", "/root/explorer/probe", true)
			dir, body := exploreFixture(t)
			store, err := openMekugiReplayStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			proxy := &mekugiProxy{activity: activity, usage: newThreadUsage()}
			ctx := proxy.exploreContext(t.Context(), thread, thread)
			judge := &fakeExploreJudge{score: func(exploreUnitState) float64 { return .01 }}
			filter := newExploreFilter(judge)
			header := "Wall time: 0.1 seconds\nProcess exited with code 0\nOutput:\n"
			request := exploreRequest(t, dir, "rg -n snapshot", header+body)
			filter.project(ctx, request, nil, dir, "", "/root", store)
			wantAgent := "/root/explorer/probe"
			if thread == "root" {
				wantAgent = "/root"
			}
			entries := activity.takeNativeActivity("root")
			if len(entries) != 1 || entries[0].Kind != "output_filter" || entries[0].Agent != wantAgent || entries[0].CallID != "call-1" {
				t.Fatalf("unexpected native annotation: %+v", entries)
			}
			event := entries[0].Filter
			if event == nil || event.Command != "rg -n snapshot" || event.Family != "paths" || event.LinesBefore != 49 || event.LinesRemoved != 24 || event.UnitsBefore != 6 || event.UnitsRemoved != 3 {
				t.Fatalf("bad filter metrics: %+v", event)
			}
			after := strings.TrimPrefix(exploreOutput(t, request), header)
			codec, err := tokenizer.New()
			if err != nil {
				t.Fatal(err)
			}
			beforeTokens, err := codec.Count(body)
			if err != nil {
				t.Fatal(err)
			}
			afterTokens, err := codec.Count(after)
			if err != nil {
				t.Fatal(err)
			}
			if event.Tokens == nil || event.Tokens.Before != beforeTokens || event.Tokens.After != afterTokens || event.Tokens.Saved != beforeTokens-afterTokens || event.Tokens.Basis != "o200k_base" || event.Tokens.Percent != 100*float64(beforeTokens-afterTokens)/float64(beforeTokens) {
				t.Fatalf("wrong net stdout token metrics: %+v", event.Tokens)
			}
			if event.BytesBefore != len(body) || event.BytesAfter != len(after) || event.JudgeUsage.InputTokens != 10 || event.JudgeUsage.Requests != 1 {
				t.Fatalf("wrong output/usage metrics: %+v", event)
			}
			for _, want := range []string{"~tokens", "24/49 lines", "→"} {
				if !strings.Contains(entries[0].Text, want) {
					t.Errorf("pane text missing %q", want)
				}
			}
			ref := regexp.MustCompile(`Full output: mread ([a-z]+[0-9]*)\]`).FindStringSubmatch(after)
			if len(ref) != 2 {
				t.Fatal("missing recovery reference")
			}
			retained, err := store.readShellOutput(t.Context(), ref[1])
			if err != nil || retained.Stdout != body {
				t.Fatal("event exposed before durable output")
			}
			replay := exploreRequest(t, dir, "rg -n snapshot", header+body)
			filter.project(ctx, replay, nil, dir, "", "/root", store)
			usage, _ := proxy.usage.snapshot(thread)
			if judge.calls.Load() != 1 || usage.typesafe.Requests != 1 || usage.typesafe.InputTokens != 10 {
				t.Fatal("replay duplicated usage")
			}
			if got := activity.takeNativeActivity("root"); len(got) != 0 {
				t.Fatalf("filter replay duplicated native annotation: %+v", got)
			}
		})
	}
}

func TestExploreFilterSubtleCommandAnnotation(t *testing.T) {
	event := exploreFilterEvent{Command: "rg needle", LinesBefore: 49, LinesRemoved: 24, UnitsBefore: 6, UnitsRemoved: 3,
		BytesBefore: 4000, BytesAfter: 2000, ElapsedMS: 627,
		Tokens: &exploreTokenReduction{Before: 1200, After: 700, Saved: 500, Percent: 100 * 500.0 / 1200, Basis: "o200k_base"}}
	if got, want := event.text(), "~tokens 1.2K→700 (-41.7%) · −24/49 lines · 0.6s"; got != want {
		t.Fatalf("compact text = %q, want %q", got, want)
	}
	for _, preceding := range []bool{false, true} {
		view := newLiveActivityView()
		now := time.Now()
		view.apply(activityPaneEvent{Kind: "snapshot", Agents: []activityPaneAgent{{Name: "/root/a"}}})
		if preceding {
			view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: 1, Agent: "/root/a", CallID: "call", Kind: "tool", Text: "Run `rg needle`", Observed: now}}})
		}
		view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: 2, Agent: "/root/a", CallID: "call", Kind: "output_filter", Text: event.text(), Filter: &event, Observed: now}}})
		filterIndex := 0
		if preceding {
			filterIndex = 1
		}
		if len(view.entries) != 1 || len(view.blocks[0]) != filterIndex+1 || view.blocks[0][filterIndex].Kind != "filter" {
			t.Fatal("metrics did not attach after command")
		}
		if strings.Contains(view.painter.Summary(view.blocks[0]), "~tokens") {
			t.Fatal("metrics replaced roster activity")
		}
		for _, width := range []int{35, 90, 120} {
			muted := strings.Join(view.painter.Block(view.blocks[0][filterIndex], width), "\n")
			if !strings.Contains(muted, activityui.Dim+"~tokens") {
				t.Fatal("metrics are not muted")
			}
			for _, line := range view.render(width, 20, now) {
				if ansi.StringWidth(line) > width {
					t.Fatalf("overflow at width %d", width)
				}
			}
		}
	}
}

func TestExploreFilterObservationKeptAndCodeMode(t *testing.T) {
	dir, body := exploreFixture(t)
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, keep := range []bool{true, false} {
		activity := newSubagentActivity()
		activity.attachNativePane("root")
		activity.observe("root", "", "/root", false)
		proxy := &mekugiProxy{activity: activity, usage: newThreadUsage()}
		judge := &fakeExploreJudge{score: func(exploreUnitState) float64 {
			if keep {
				return .9
			}
			return .01
		}}
		filter := newExploreFilter(judge)
		output := exploreCodeModeBlocks(exploreCodeModeHeader, string(mustTestJSON(t, exploreCodeModeResult(t, 0, body))))
		request := exploreCodeModeRequest(t, exploreCodeModeSource("rg -n snapshot", dir), "functions", output)
		before := slices.Clone(request.fields["input"])
		filter.project(proxy.exploreContext(t.Context(), "root", "root"), request, nil, dir, "", "/root", store)
		usage, _ := proxy.usage.snapshot("root")
		if usage.typesafe.Requests != 1 || usage.typesafe.InputTokens != 10 {
			t.Fatal("kept judgment usage lost")
		}
		if keep {
			if !bytes.Equal(before, request.fields["input"]) || len(activity.events) != 0 {
				t.Fatal("kept output emitted compaction")
			}
		} else {
			if len(activity.events) != 1 || activity.events[0].filter == nil {
				t.Fatal("Code Mode filter event missing")
			}
			_, result := exploreCodeModeParts(t, request)
			var nested struct {
				Output string `json:"output"`
			}
			if err := json.Unmarshal([]byte(result), &nested); err != nil {
				t.Fatal(err)
			}
			if activity.events[0].filter.BytesAfter != len(nested.Output) {
				t.Fatal("counted JSON wrapper as stdout")
			}
		}
		if got := activity.takeNativeActivity("root"); len(got) != map[bool]int{true: 0, false: 1}[keep] {
			t.Fatalf("native filter annotations = %+v", got)
		}
	}
}

func TestExploreFilterNativePayloadDedupAndIsolation(t *testing.T) {
	a := newSubagentActivity()
	a.attachNativePane("root")
	a.observe("root", "", "/root", false)
	a.observe("other", "", "/root", false)
	event := activityEvent{thread: "root", source: "filter-one", kind: "output_filter", callID: "call-1", text: "Output filtered", filter: &exploreFilterEvent{Command: "rg needle"}}
	a.collectEvent(event)
	a.collectEvent(event)
	event.thread, event.source = "other", "filter-other"
	a.collectEvent(event)
	entries := a.takeNativeActivity("root")
	if len(entries) != 1 || entries[0].Filter == nil || entries[0].Filter.Command != "rg needle" || entries[0].CallID != "call-1" {
		t.Fatalf("duplicate or cross-root annotation: %+v", entries)
	}
	if got := a.takeNativeActivity("root"); len(got) != 0 {
		t.Fatalf("native annotation repeated: %+v", got)
	}
	a.invalidate("root")
	event.thread, event.source = "root", "conflicted-filter"
	a.collectEvent(event)
	if got := a.takeNativeActivity("root"); len(got) != 0 {
		t.Fatalf("conflicted annotation escaped: %+v", got)
	}
}
