package terminalview

import (
	"testing"

	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/modules/changehistory/presentation"
)

func TestLiveDiffHunkNavigationAndFileMemory(t *testing.T) {
	c := NewPane("", diffrender.DarkTheme)
	c.files = []presentation.File{{Path: "a.go", Chunks: []presentation.Chunk{{Key: "a", Review: diffmodel.ReviewFile{BeforePath: "a.go", AfterPath: "a.go", Diff: "@@ -1 +1 @@\n-old\n+new\n@@ -20 +20 @@\n-before\n+after\n"}}}}, {Path: "b.go", Chunks: []presentation.Chunk{{Key: "b", Review: diffmodel.RenderReviewFile("", "b.go", "", "second\n")}}}}
	c.view.Files = c.files
	var err error
	c.rendering, err = c.renderer.Render(t.Context(), diffrender.DarkTheme, c.files, "", 80)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.rendering.Hunks) != 3 {
		t.Fatalf("hunk offsets: %v", c.rendering.Hunks)
	}
	c.navigation.Rebuild(c.files, "")
	c.lines, c.rows = c.rendering.Lines, 8
	c.Key(string(']'))
	first := c.view.Scroll[c.files[0].Key()]
	if first != c.rendering.Hunks[0] {
		t.Fatalf("next hunk at %d, want %d", first, c.rendering.Hunks[0])
	}
	c.offset = first
	c.Key(string(']'))
	second := c.view.Scroll[c.files[0].Key()]
	if second != c.rendering.Hunks[1] {
		t.Fatalf("second hunk at %d", second)
	}
	c.Key(string('n'))
	c.Key(string('p'))
	if c.view.Selected != 0 || c.view.Scroll[c.files[0].Key()] != second {
		t.Fatal("file switching lost scroll memory")
	}
	c.offset = second
	c.Key(string('['))
	if c.view.Scroll[c.files[0].Key()] != first {
		t.Fatal("previous hunk missed anchor")
	}
}
