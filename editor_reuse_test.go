package mekugi

import (
	"reflect"
	"strings"
	"testing"
)

func TestRecordEditsPublishesOnlyValidatedBatch(t *testing.T) {
	e := editor{baseline: "abcdef"}
	first := baselineEdit{start: 1, end: 3, replacement: "BC", editOrigin: editOrigin{command: 1}}
	if err := e.recordEdits([]baselineEdit{first}); err != nil {
		t.Fatal(err)
	}
	before := e.edits
	beforeContent := e.content()
	beforeProjection := e.projected
	beforeOrigin := e.lastOrigin
	err := e.recordEdits([]baselineEdit{
		{start: 5, end: 6, replacement: "F", editOrigin: editOrigin{command: 2}},
		{start: 2, end: 4, replacement: "CD", editOrigin: editOrigin{command: 2}},
	})
	if err == nil {
		t.Fatal("conflicting batch accepted")
	}
	if !reflect.DeepEqual(e.edits, before) || e.content() != beforeContent ||
		!reflect.DeepEqual(e.projected, beforeProjection) || !reflect.DeepEqual(e.lastOrigin, beforeOrigin) {
		t.Fatal("rejected batch changed editor state")
	}
	if err := e.recordEdits([]baselineEdit{
		{start: 0, end: 0},
		{start: 0, end: 1, replacement: "a"},
		{start: 3, end: 3, replacement: "1", editOrigin: editOrigin{command: 3}},
		{start: 3, end: 3, replacement: "2", editOrigin: editOrigin{command: 4}},
	}); err != nil {
		t.Fatal(err)
	}
	if got := e.content(); got != "aBC12def" {
		t.Fatalf("content = %q", got)
	}
	if len(e.edits) != 3 || e.edits[1].sequence != 2 || e.edits[2].sequence != 3 || e.lastOrigin.command != 4 {
		t.Fatalf("published edits = %+v, origin = %+v", e.edits, e.lastOrigin)
	}
}

func BenchmarkFinalizedEditorContent(b *testing.B) {
	file := fileState{path: "large.txt", editor: editor{baseline: strings.Repeat("line\n", 10000)}}
	if err := file.editor.recordEdits([]baselineEdit{{start: 0, end: 4, replacement: "changed"}}); err != nil {
		b.Fatal(err)
	}
	failures, err := file.renderContent(b.Context())
	if err != nil || len(failures) != 0 {
		b.Fatalf("render: %v, %v", failures, err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if len(file.editor.content()) != 50003 {
			b.Fatal("unexpected content")
		}
	}
}

func BenchmarkLogicalLines(b *testing.B) {
	source := strings.Repeat("one\r\ntwo\rthree\n", 1000)
	b.ReportAllocs()
	for b.Loop() {
		if len(logicalLines(source)) != 3000 {
			b.Fatal("unexpected rows")
		}
	}
}

func TestFinalizedEditorContent(t *testing.T) {
	for _, tc := range []struct{ path, source, want string }{
		{"plain.txt", "a\r\nb\rc\n", "A\r\nb\rc\n"},
		{"empty.txt", "a", ""},
		{"formatted.go", "package main\nvar a = 1\n", "package main\n\nvar A = 1\n"},
		{"unchanged.go", "package main\n\nvar a = 1\n", "package main\n\nvar A = 1\n"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			replacement := "A"
			if tc.path == "empty.txt" {
				replacement = ""
			}
			offset := strings.LastIndex(tc.source, "a")
			file := fileState{path: tc.path, originalPath: tc.path, original: tc.source, editor: editor{baseline: tc.source}}
			if err := file.editor.recordEdits([]baselineEdit{{start: offset, end: offset + 1, replacement: replacement}}); err != nil {
				t.Fatal(err)
			}
			failures, err := file.renderContent(t.Context())
			if err != nil || len(failures) != 0 {
				t.Fatalf("render: %v, %v", failures, err)
			}
			for range 3 {
				if got := file.editor.content(); got != tc.want {
					t.Fatalf("content = %q, want %q", got, tc.want)
				}
			}
			if (file.editor.finalOffsets != nil) != (tc.path == "formatted.go") {
				t.Fatal("formatting offset policy changed")
			}
		})
	}
}
