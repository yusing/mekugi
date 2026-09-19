package mekugi

import (
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestEditorProjectionPreservesSpliceOrderingAndByteSpans(t *testing.T) {
	source := []baselineEdit{
		{start: 7, end: 10, sequence: 4, editOrigin: editOrigin{command: 3}},
		{start: 3, end: 7, replacement: "β\n", sequence: 2, editOrigin: editOrigin{command: 2}},
		{start: 3, end: 3, replacement: "also\n", sequence: 3, editOrigin: editOrigin{command: 1}},
		{start: 3, end: 3, replacement: "before\n", sequence: 1, editOrigin: editOrigin{command: 1}},
	}
	e := editor{baseline: "α\nold\nend", edits: source}
	projection := e.renderedEdits()
	wantSpans := []renderedSpan{{3, 10}, {10, 15}, {15, 18}, {18, 18}}
	wantSequences := []int{1, 3, 2, 4}
	content := e.content()
	if content != "α\nbefore\nalso\nβ\n" {
		t.Fatalf("content = %q", content)
	}
	for i, edit := range projection {
		if edit.span != wantSpans[i] || edit.sequence != wantSequences[i] || content[edit.span.start:edit.span.end] != edit.replacement {
			t.Fatalf("projection[%d] = %+v, want span %+v, sequence %d", i, edit, wantSpans[i], wantSequences[i])
		}
	}
	if source[0].sequence != 4 || source[3].sequence != 1 {
		t.Fatal("projection reordered its input snapshot")
	}
	if extents := e.renderedEditExtents(); extents[1] != (renderedSpan{3, 15}) || extents[3] != (renderedSpan{18, 18}) {
		t.Fatalf("command extents = %+v", extents)
	}
	groups := e.syntaxEditGroups(18, len(content))
	if nearest := closestSyntaxEditGroup(groups); nearest.origin.command != 3 || nearest.distance != 0 {
		t.Fatalf("deletion-boundary syntax attribution = %+v", nearest)
	}
}

func TestEditorProjectionTracksMutationSnapshots(t *testing.T) {
	e := editor{baseline: "a\nb\n"}
	if err := e.recordEdits([]baselineEdit{{start: 2, end: 4, replacement: "B\n"}}); err != nil {
		t.Fatal(err)
	}
	if got := e.content(); got != "a\nB\n" {
		t.Fatalf("first content = %q", got)
	}
	probe := slices.Clone(e.edits)
	probe[0].replacement = "longer probe\n"
	if got := e.contentWithEdits(probe); got != "a\nlonger probe\n" {
		t.Fatalf("probe content = %q", got)
	}
	if got := e.content(); got != "a\nB\n" {
		t.Fatalf("probe changed editor snapshot: %q", got)
	}
	if err := e.recordEdits([]baselineEdit{{start: 0, end: 0, replacement: "x\n"}}); err != nil {
		t.Fatal(err)
	}
	if got := e.content(); got != "x\na\nB\n" || e.renderedEdits()[1].span != (renderedSpan{4, 6}) {
		t.Fatalf("added edit retained stale projection: content %q, edits %+v", got, e.renderedEdits())
	}
	if err := e.recordEdits([]baselineEdit{{start: 2, end: 4, replacement: "conflict\n"}}); err == nil {
		t.Fatal("conflicting edit succeeded")
	}
	if got := e.content(); got != "x\na\nB\n" {
		t.Fatalf("rejected edit changed snapshot: %q", got)
	}
}

func TestEditorProjectionRefreshesAfterIndentation(t *testing.T) {
	for _, test := range []struct {
		name, baseline, target, replacement, corrected string
	}{
		{"exact", "def f():\n    value()\n", row(2, "    value()"), "value()\n", "    value()\n"},
		{"wrapper", "def f():\n    if ready:\n        existing()\n    return\n", row(3, "        existing()"), "        if ready:\n        existing()\n", "        if ready:\n            existing()\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			e := editor{baseline: test.baseline}
			program, err := parse("type " + test.target + " " + strconv.Quote(test.replacement))
			if err != nil {
				t.Fatal(err)
			}
			command := program.instructions[0]
			if err := e.applyMutation(command.operation, command.target, command.text, editOrigin{command: 1}, command, "file.py"); err != nil {
				t.Fatal(err)
			}
			before := e.content() // Populate the projection before correction.
			if err := e.renderIndentation(t.Context(), "file.py"); err != nil {
				t.Fatal(err)
			}
			want := strings.Replace(before, test.replacement, test.corrected, 1)
			if got := e.content(); got != want {
				t.Fatalf("corrected content %q, want %q", got, want)
			}
			span := e.renderedEdits()[0].span
			if span.end-span.start != len(test.corrected) || e.content()[span.start:span.end] != test.corrected {
				t.Fatalf("corrected replacement span %+v", span)
			}
		})
	}
}
