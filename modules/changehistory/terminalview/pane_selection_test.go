package terminalview

import (
	"bytes"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/modules/changehistory/presentation"
)

func TestPaneSelectionPinnedHeadingAndFrozenCopies(t *testing.T) {
	for _, width := range []int{70, 120} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			pane := NewPane("/w", diffrender.DarkTheme)
			pane.Update(presentation.GroupCaptures([]presentation.Chunk{
				liveDiffCapture("one", "a.go", 1, "", liveDiffLinesFile("a.go", 40), presentation.Origin{Change: "amber1", Caller: "/root"}),
				liveDiffCapture("two", "b.go", 2, "", "second\n", presentation.Origin{Change: "apple1", Caller: "/root/worker"}),
			}), "captured")
			var output bytes.Buffer
			paint := func(w int) {
				t.Helper()
				output.Reset()
				if err := pane.Paint(t.Context(), &output, w, 18); err != nil {
					t.Fatal(err)
				}
			}
			paint(width)
			var rejected SelectionSource
			if allocations := testing.AllocsPerRun(20, func() { rejected = pane.Selection(0, 0) }); allocations != 0 || len(rejected.Painted) != 0 || len(rejected.Document) != 0 {
				t.Fatalf("navigator hit materialized selection: allocations=%g, source=%+v", allocations, rejected)
			}
			initial := pane.Selection(width-2, 17)
			if initial.Fixed != 0 || initial.Offset != 0 || len(initial.Document) <= len(initial.Painted) {
				t.Fatalf("initial selection lacks the complete scrollable document: %+v", initial)
			}
			target := slices.IndexFunc(initial.Sources, func(source diffrender.LineSource) bool {
				return source.Path == "/w/a.go" && source.Line == 8 && !source.Deleted
			})
			if target <= 0 {
				t.Fatal("painted document lacks a.go line 8")
			}
			for range target {
				pane.Key("j")
			}
			paint(width)
			frozen := pane.Selection(width-2, 17)
			if frozen.Fixed != 1 || frozen.Offset != target || len(frozen.Document) != len(initial.Document)+1 || len(frozen.Sources) != len(frozen.Document) {
				t.Fatalf("pinned selection lost document geometry: fixed=%d offset=%d document=%d sources=%d", frozen.Fixed, frozen.Offset, len(frozen.Document), len(frozen.Sources))
			}
			if frozen.Width != width-frozen.X || frozen.Y < 0 || len(frozen.Painted) < 2 {
				t.Fatalf("invalid painted rectangle: %+v", frozen)
			}
			if width == 70 && (frozen.X != 0 || frozen.Y == 0) || width == 120 && (frozen.X == 0 || frozen.Y != 0) {
				t.Fatalf("navigator geometry at width %d: x=%d y=%d", width, frozen.X, frozen.Y)
			}
			if !strings.Contains(ansi.Strip(frozen.Document[0]), "a.go") || frozen.Painted[0] != frozen.Sources[0] || frozen.Painted[0].Line != 0 || frozen.Painted[0].Path != "/w/a.go" {
				t.Fatalf("pinned heading attribution: painted=%+v source=%+v", frozen.Painted[0], frozen.Sources[0])
			}
			line := frozen.Painted[1]
			if line.Line != 8 || line.Path != "/w/a.go" || line.Change != "amber1" || line.Deleted || line != frozen.Sources[frozen.Offset+frozen.Fixed] {
				t.Fatalf("first source below the pin has wrong attribution: %+v", line)
			}
			for i := range 2 {
				documentRow := 0
				if i > 0 {
					documentRow = frozen.Offset + frozen.Fixed
				}
				text := strings.TrimSpace(ansi.Strip(frozen.Document[documentRow]))
				if row := liveDiffFrameRow(output.String(), frozen.Y+i+1); !strings.Contains(row, text) {
					t.Fatalf("selection row %d differs from the painted frame: %q, want %q", i, row, text)
				}
			}
			want := frozen
			want.Document, want.Sources, want.Painted = slices.Clone(frozen.Document), slices.Clone(frozen.Sources), slices.Clone(frozen.Painted)
			probe := pane.Selection(width-2, 17)
			probe.Document[1] = "changed by selection consumer"
			probe.Sources[1].Path = "/consumer"
			probe.Painted[1].Path = "/consumer"
			if !reflect.DeepEqual(pane.Selection(width-2, 17), want) {
				t.Fatal("selection slices alias the pane's current document or attribution")
			}
			pane.SetWorkspace("/next")
			pane.Key("n")
			if !reflect.DeepEqual(pane.Selection(width-2, 17), want) {
				t.Fatal("unpainted workspace or file navigation changed the displayed selection")
			}
			pane.Key("G")
			paint(width + 30)
			pane.Update(presentation.GroupCaptures([]presentation.Chunk{
				liveDiffCapture("replacement", "c.go", 3, "", "replacement\n", presentation.Origin{Change: "arch1"}),
			}), "replaced")
			paint(width)
			pane.Reset()
			if !reflect.DeepEqual(frozen, want) {
				t.Fatal("scroll, resize, replacement or reset mutated the frozen selection")
			}
		})
	}
}
