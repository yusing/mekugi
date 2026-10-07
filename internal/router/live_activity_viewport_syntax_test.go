package router

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

// Separate operations with commentary so Main has independent transcript
// items, while Activity groups each pair under a distinct agent heading.
func viewportSyntaxView(main bool, lang string) *liveActivityView {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)
	v := newLiveActivityView()
	v.conversation, v.feedOnly = main, true
	v.painter.Theme = livediff.DarkTheme
	v.clock = func() time.Time { return now }
	for i := range 12 {
		agent := fmt.Sprintf("/root/source%d", i)
		if main {
			agent = "Main"
		}
		seq := uint64(2*i + 1)
		body := fmt.Sprintf("Inspect source %d before continuing.", i)
		v.appendEntry(activityPaneEntry{Seq: seq, Agent: agent, Kind: "text", Text: body, Observed: now},
			[]activityui.Block{{Kind: "text", Body: body}})
		v.appendEntry(activityPaneEntry{Seq: seq + 1, Agent: agent, Kind: "tool", Text: "Run source preview", Observed: now},
			[]activityui.Block{{Kind: "op", Verb: "Run", Label: "source preview", Lang: lang, Fenced: true,
				Code: fmt.Sprintf("package source%d\n\nfunc value%d() int {\n\treturn %d\n}\n", i, i, i)}})
		v.lastSeq = seq + 1
	}
	return v
}

// Off-screen ANSI intentionally differs. Everything else, including full-feed
// scroll geometry and navigation targets, must equal a fresh eager render.
func assertViewportSyntaxEager(t *testing.T, lazy, eager *liveActivityView, width, rows int) {
	t.Helper()
	eager.runs = nil
	got := lazy.renderFeed(width, rows)
	want := eager.layoutFeed(width, rows)
	if !reflect.DeepEqual(threadPlain(got), threadPlain(want)) ||
		!reflect.DeepEqual(got.heads, want.heads) ||
		!reflect.DeepEqual(got.snippets, want.snippets) ||
		!reflect.DeepEqual(got.questions, want.questions) ||
		!reflect.DeepEqual(got.passing, want.passing) ||
		!reflect.DeepEqual(lazy.questionRows, eager.questionRows) {
		t.Fatalf("deferred feed changed geometry or navigation:\ngot: %#v\nwant: %#v", got, want)
	}
	for key, run := range lazy.runs {
		reference, ok := eager.runs[key]
		if !ok || !reflect.DeepEqual(run.blocks, reference.blocks) ||
			!reflect.DeepEqual(run.entryRows, reference.entryRows) ||
			!reflect.DeepEqual(run.snippets, reference.snippets) ||
			!reflect.DeepEqual(run.questions, reference.questions) {
			t.Fatalf("run %v changed dialog or navigation metadata", key)
		}
	}
	gotRows, wantRows := lazy.viewport(got, rows), eager.viewport(want, rows)
	if !reflect.DeepEqual(gotRows, wantRows) {
		t.Fatalf("visible ANSI differs from eager render:\ngot: %q\nwant: %q", gotRows, wantRows)
	}
	if lazy.offset != eager.offset || lazy.following != eager.following ||
		lazy.feedLines != eager.feedLines || lazy.feedRows != eager.feedRows ||
		!reflect.DeepEqual(lazy.feedSnippets, eager.feedSnippets) ||
		!reflect.DeepEqual(lazy.feedQuestions, eager.feedQuestions) ||
		!reflect.DeepEqual(lazy.passed, eager.passed) {
		t.Fatal("deferred viewport changed scrolling or pointer targets")
	}
}

func TestLiveActivityViewportSyntaxMatchesEager(t *testing.T) {
	t.Parallel()
	for _, main := range []bool{false, true} {
		t.Run(fmt.Sprintf("Main=%t", main), func(t *testing.T) {
			lazy, eager := viewportSyntaxView(main, "go"), viewportSyntaxView(main, "go")
			for _, v := range []*liveActivityView{lazy, eager} {
				v.entries[9].blocks[0].Lang = "diff"
				v.entries[9].blocks[0].Code = "diff --git a/source.go b/source.go\n--- a/source.go\n+++ b/source.go\n@@ -1,2 +1,2 @@\n package source\n-var value = 1\n+var value = 2\n"
				v.entries[11].blocks = append(v.entries[11].blocks, activityui.Block{Kind: "op", Verb: "Ask", Label: "1 question", Questions: []activityui.Question{{Text: "Continue the inspection?", State: "answered", Answer: "Yes"}}})
			}
			check := func(name string, width, rows int) {
				t.Run(name, func(t *testing.T) { assertViewportSyntaxEager(t, lazy, eager, width, rows) })
			}
			check("cold seek at end", 70, 9)
			check("repeated end", 70, 9)
			for _, v := range []*liveActivityView{lazy, eager} {
				v.following, v.offset = false, 2
			}
			check("scroll inside early run", 70, 9)
			for _, v := range []*liveActivityView{lazy, eager} {
				if _, ok := v.questionRows[12]; !ok {
					t.Fatal("fixture lacks the cross-pane navigation target")
				}
				v.pendingTarget = 12
			}
			check("cross pane jump", 70, 9)
			check("narrow resize", 34, 6)
			for _, v := range []*liveActivityView{lazy, eager} {
				v.painter.Theme = livediff.LightTheme
			}
			check("theme change", 34, 6)
			for _, v := range []*liveActivityView{lazy, eager} {
				v.following = true
			}
			check("follow end after resize", 90, 12)
			for _, v := range []*liveActivityView{lazy, eager} {
				v.entries[1].blocks[0].Code = "package changed\n\nfunc updated() string { return \"new source\" }\n"
				v.invalidateEntry(2)
			}
			check("off screen update", 90, 12)
			for _, v := range []*liveActivityView{lazy, eager} {
				v.following, v.offset = false, 0
			}
			check("updated source enters viewport", 90, 12)
		})
	}
}

type viewportSyntaxCountingLexer struct {
	chroma.Lexer
	sources map[string]int
}

func (l *viewportSyntaxCountingLexer) Tokenise(options *chroma.TokeniseOptions, source string) (chroma.Iterator, error) {
	l.sources[source]++
	return l.Lexer.Tokenise(options, source)
}

func registerViewportSyntaxCountingLexer(t *testing.T) *viewportSyntaxCountingLexer {
	t.Helper()
	// This test is deliberately not parallel: restore the shared lexer registry
	// after using a private extension, without modifying built-in lexers.
	original := lexers.GlobalLexerRegistry
	lexers.GlobalLexerRegistry = chroma.NewLexerRegistry()
	t.Cleanup(func() { lexers.GlobalLexerRegistry = original })
	lexer := &viewportSyntaxCountingLexer{Lexer: chroma.MustNewLexer(&chroma.Config{
		Name: "ViewportFixture", Filenames: []string{"*.viewportfixture"},
	}, func() chroma.Rules {
		return chroma.Rules{"root": {{Pattern: `(?s).+`, Type: chroma.Keyword}}}
	}), sources: make(map[string]int)}
	lexers.Register(lexer)
	return lexer
}

func TestLiveActivityViewportSyntaxDefersTokenization(t *testing.T) {
	lexer := registerViewportSyntaxCountingLexer(t)
	for _, main := range []bool{false, true} {
		t.Run(fmt.Sprintf("Main=%t", main), func(t *testing.T) {
			clear(lexer.sources)
			v := viewportSyntaxView(main, "viewportfixture")
			first := strings.ReplaceAll(v.entries[1].blocks[0].Code, "\t", "    ")
			last := strings.ReplaceAll(v.entries[len(v.entries)-1].blocks[0].Code, "\t", "    ")
			feed := v.renderFeed(70, 9)
			v.viewport(feed, 9)
			if lexer.sources[first] != 0 || lexer.sources[last] != 1 {
				t.Fatalf("cold end tokenizations: first=%d last=%d, want 0 and 1", lexer.sources[first], lexer.sources[last])
			}
			for key, run := range v.runs {
				if key.first == 1 && run.colored {
					t.Fatal("cold off-screen run was decorated")
				}
			}
			before := make(map[string]int, len(lexer.sources))
			for source, count := range lexer.sources {
				before[source] = count
			}
			v.renderFeed(70, 9)
			if !reflect.DeepEqual(before, lexer.sources) {
				t.Fatal("unchanged end viewport retokenized source")
			}
			v.following, v.offset = false, 0
			feed = v.renderFeed(70, 9)
			v.viewport(feed, 9)
			if lexer.sources[first] != 1 {
				t.Fatalf("entering early viewport tokenized first source %d times, want 1", lexer.sources[first])
			}
			v.renderFeed(70, 9)
			if lexer.sources[first] != 1 || lexer.sources[last] != 1 {
				t.Fatal("repaint retokenized retained source")
			}
			if text := strings.Join(v.viewport(v.renderFeed(70, 9), 9), "\n"); text == ansi.Strip(text) {
				t.Fatal("visible viewport lost ANSI styling")
			}
			v.following = true
			v.viewport(v.renderFeed(70, 9), 9)
			changed := "package updated\n\nfunc updated() int { return 42 }\n"
			v.entries[1].blocks[0].Code = changed
			v.invalidateEntry(2)
			v.renderFeed(70, 9)
			if lexer.sources[changed] != 0 {
				t.Fatal("off-screen update was tokenized before becoming visible")
			}
			v.following, v.offset = false, 0
			v.renderFeed(70, 9)
			if lexer.sources[changed] != 1 {
				t.Fatalf("updated source entering viewport tokenized %d times, want 1", lexer.sources[changed])
			}
		})
	}
}

// Real replay history can keep hundreds of operations in one run. A run-level
// viewport gate is insufficient: only intersecting blocks may be tokenized.
func groupedViewportSyntaxView(main bool, lang string) *liveActivityView {
	v := viewportSyntaxView(main, lang)
	records := make([]liveActivityRecord, 0, len(v.entries)/2)
	for i := 1; i < len(v.entries); i += 2 {
		record := v.entries[i]
		if !main {
			record.Agent = "/root/source"
		}
		records = append(records, record)
	}
	v.entries = records
	return v
}

func TestLiveActivityViewportSyntaxGroupedBlocks(t *testing.T) {
	lexer := registerViewportSyntaxCountingLexer(t)
	for _, mode := range []struct{ main, children bool }{{}, {children: true}, {main: true}} {
		t.Run(fmt.Sprintf("Main=%t/children=%t", mode.main, mode.children), func(t *testing.T) {
			clear(lexer.sources)
			v := groupedViewportSyntaxView(mode.main, "viewportfixture")
			v.childrenOnly = mode.children
			first := strings.ReplaceAll(v.entries[0].blocks[0].Code, "\t", "    ")
			last := strings.ReplaceAll(v.entries[len(v.entries)-1].blocks[0].Code, "\t", "    ")
			v.viewport(v.renderFeed(70, 9), 9)
			if len(v.runs) != 1 {
				t.Fatalf("fixture produced %d runs, want one grouped run", len(v.runs))
			}
			if lexer.sources[first] != 0 || lexer.sources[last] != 1 || len(lexer.sources) >= len(v.entries) {
				t.Fatalf("cold grouped end tokenizations: first=%d last=%d sources=%d, want deferred early blocks", lexer.sources[first], lexer.sources[last], len(lexer.sources))
			}
			coldSources := len(lexer.sources)
			v.renderFeed(70, 9)
			if len(lexer.sources) != coldSources || lexer.sources[last] != 1 {
				t.Fatal("grouped end repaint tokenized additional blocks")
			}
			v.following, v.offset = false, 0
			v.viewport(v.renderFeed(70, 9), 9)
			if lexer.sources[first] != 1 || lexer.sources[last] != 1 || len(lexer.sources) >= len(v.entries) {
				t.Fatalf("early grouped scroll tokenizations: first=%d last=%d sources=%d, want only newly visible blocks", lexer.sources[first], lexer.sources[last], len(lexer.sources))
			}
			earlySources := len(lexer.sources)
			v.renderFeed(70, 9)
			if len(lexer.sources) != earlySources || lexer.sources[first] != 1 {
				t.Fatal("grouped early repaint retokenized source")
			}
			// Compare only after counting assertions: the eager reference must
			// tokenize all blocks, but cannot hide excessive lazy tokenization.
			eager := groupedViewportSyntaxView(mode.main, "viewportfixture")
			eager.childrenOnly = mode.children
			eager.following, eager.offset = false, 0
			assertViewportSyntaxEager(t, v, eager, 70, 9)
			for _, view := range []*liveActivityView{v, eager} {
				view.following = true
			}
			assertViewportSyntaxEager(t, v, eager, 70, 9)
			assertViewportSyntaxEager(t, v, eager, 34, 6)
		})
	}
}

func TestUISnapshotLiveActivityViewportSyntaxRepaint(t *testing.T) {
	t.Parallel()
	for _, main := range []bool{false, true} {
		t.Run(fmt.Sprintf("Main=%t", main), func(t *testing.T) {
			v := viewportSyntaxView(main, "go")
			var snapshot strings.Builder
			for _, frame := range []struct {
				label  string
				follow bool
				offset int
			}{
				{"cold end", true, 0},
				{"early scroll", false, 2},
				{"early repaint", false, 2},
			} {
				v.following, v.offset = frame.follow, frame.offset
				fmt.Fprintf(&snapshot, "== %s ==\n", frame.label)
				snapshot.WriteString(strings.Join(v.render(60, 12, v.now()), "\n"))
				snapshot.WriteByte('\n')
			}
			name := "activity"
			if main {
				name = "main"
			}
			uisnapshot.Assert(t, "testdata/snapshots/viewport-syntax-"+name+".txt", snapshot.String())
		})
	}
}

func TestLiveActivityViewportSyntaxScrollEveryRow(t *testing.T) {
	t.Parallel()
	for _, main := range []bool{false, true} {
		t.Run(fmt.Sprintf("Main=%t", main), func(t *testing.T) {
			build := func() *liveActivityView {
				v := groupedViewportSyntaxView(main, "go")
				if main {
					// A tool group after agent traffic continues an earlier lead
					// that already headed a read. Its extra heading shifts every
					// block's syntax window by one row.
					prefix := []liveActivityRecord{
						{Seq: 1, Agent: "Main", Kind: "reasoning", Text: "**Preparing regression seed**", Observed: v.now()},
						{Seq: 2, Agent: "Main", Kind: "tool", Text: "Read `main.go 1:2`", Observed: v.now()},
						{Seq: 3, Agent: "/root/lookup", Kind: "final", Text: "Evidence collected.", Observed: v.now()},
					}
					for i := range prefix {
						prefix[i].blocks = parseLiveActivity(prefix[i].activityPaneEntry)
					}
					for i := range v.entries {
						v.entries[i].Seq += 4
					}
					v.entries = append(prefix, v.entries...)
				}
				return v
			}
			lazy, eager := build(), build()
			full := eager.layoutFeed(70, 3)
			if main {
				continued := false
				for key := range eager.runs {
					continued = continued || key.lead != 0
				}
				if !continued {
					t.Fatal("fixture lacks a continuation-headed tool group")
				}
			}
			for _, forward := range []bool{true, false} {
				for i := range len(full.lines) {
					offset := i
					if !forward {
						offset = len(full.lines) - 1 - i
					}
					lazy.following, eager.following = false, false
					lazy.offset, eager.offset = offset, offset
					assertViewportSyntaxEager(t, lazy, eager, 70, 3)
				}
			}
		})
	}
}
