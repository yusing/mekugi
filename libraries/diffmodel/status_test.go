package diffmodel_test

import (
	"testing"

	"github.com/yusing/mekugi/libraries/diffmodel"
)

func TestStatusClassification(t *testing.T) {
	text := diffmodel.RenderReviewFile
	binary := diffmodel.RenderBinaryReviewFile
	unknown := diffmodel.RenderIncompleteReviewFile
	directory := unknown("old", "new", "directory contents unavailable")
	directory.Directory = true
	for _, tc := range []struct {
		name    string
		regions []diffmodel.ReviewFile
		want    diffmodel.Status
		code    string
	}{
		{"add empty file", []diffmodel.ReviewFile{text("", "new", "", "")}, diffmodel.Status{After: "new"}, "A"},
		{"delete", []diffmodel.ReviewFile{text("old", "", "a\n", "")}, diffmodel.Status{Before: "old", Edited: true}, "D"},
		{"modify", []diffmodel.ReviewFile{text("file", "file", "a\n", "b\n")}, diffmodel.Status{Before: "file", After: "file", Edited: true}, "M"},
		{"move", []diffmodel.ReviewFile{text("old", "new", "a\n", "a\n")}, diffmodel.Status{Before: "old", After: "new"}, "R"},
		{"capture order", []diffmodel.ReviewFile{
			text("old", "middle", "a\n", "a\n"),
			text("middle", "middle", "a\n", "b\n"),
			text("middle", "new", "b\n", "b\n"),
		}, diffmodel.Status{Before: "old", After: "new", Edited: true}, "RM"},
		{"add then move", []diffmodel.ReviewFile{
			text("", "old", "", "a\n"), text("old", "new", "a\n", "a\n"),
		}, diffmodel.Status{After: "new", Edited: true}, "A"},
		{"move then delete", []diffmodel.ReviewFile{
			text("old", "new", "a\n", "a\n"), text("new", "", "a\n", ""),
		}, diffmodel.Status{Before: "old", Edited: true}, "D"},
		{"binary move", []diffmodel.ReviewFile{binary("old", "new", 3, 3, "abc", "abc")}, diffmodel.Status{Before: "old", After: "new"}, "R"},
		{"binary edit", []diffmodel.ReviewFile{binary("old", "new", 3, 3, "abc", "def")}, diffmodel.Status{Before: "old", After: "new", Edited: true}, "RM"},
		{"incomplete stays unknown", []diffmodel.ReviewFile{
			unknown("old", "middle", "unreadable"), text("middle", "new", "a\n", "a\n"),
		}, diffmodel.Status{Before: "old", After: "new", Incomplete: true}, "?"},
		{"directory metadata", []diffmodel.ReviewFile{directory}, diffmodel.Status{Before: "old", After: "new", Directory: true, Edited: true}, "RM"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := diffmodel.StatusOf(tc.regions...)
			if got != tc.want || got.ShortCode() != tc.code {
				t.Fatalf("status = %+v (%s), want %+v (%s)", got, got.ShortCode(), tc.want, tc.code)
			}
			incremental := diffmodel.StatusOf(tc.regions[0])
			for _, region := range tc.regions[1:] {
				incremental.Add(region)
			}
			if incremental != tc.want {
				t.Errorf("incremental status = %+v, want %+v", incremental, tc.want)
			}
		})
	}
}

func TestStatusConflictMarkers(t *testing.T) {
	edit := diffmodel.RenderReviewFile("file", "file", "base\n", "agent\n")
	merged, err := edit.Merge("workspace\n", true, "mchanges revert amber1")
	if err != nil || merged.Conflicts != 1 {
		t.Fatalf("merge = %+v, %v", merged, err)
	}
	for _, tc := range []struct {
		name, before, after string
		incomplete          bool
		conflict            bool
		code                string
	}{
		{"added markers", "workspace\n", merged.Content, false, true, "UU"},
		{"context markers", merged.Content + "old\n", merged.Content + "new\n", false, false, "M"},
		{"resolved markers", merged.Content, "resolved\n", false, false, "M"},
		{"unknown overrides conflict", "workspace\n", merged.Content, true, true, "?"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := diffmodel.RenderReviewFile("file", "file", tc.before, tc.after)
			if tc.incomplete {
				file.Incomplete = "partial capture"
			}
			got := diffmodel.StatusOf(file)
			if got.Conflict != tc.conflict || got.ShortCode() != tc.code {
				t.Errorf("status = %+v (%s), want conflict=%v (%s)", got, got.ShortCode(), tc.conflict, tc.code)
			}
		})
	}
}
