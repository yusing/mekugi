package livediff

import (
	"testing"

	"github.com/yusing/mekugi"
)

func TestAlignHunkColorsAdvancesEachSourceSide(t *testing.T) {
	kinds := []byte{' ', '-', '+', '-', '+', ' ', '+', '-'}
	rows := make([]mekugi.ReviewRow, len(kinds))
	for i, kind := range kinds {
		rows[i] = mekugi.ReviewRow{Kind: kind, Text: "uncolored"}
	}
	before := []string{"old context", "removed one", "removed two", "old next context", "removed three"}
	after := []string{"new context", "added one", "added two", "new next context", "added three"}
	want := []string{"new context", "removed one", "added one", "removed two", "added two", "new next context", "added three", "removed three"}
	AlignHunkColors(rows, before, after)
	for i, row := range rows {
		if row.Kind != kinds[i] || row.Text != want[i] {
			t.Errorf("row %d = %+v, want kind %q and text %q", i, row, kinds[i], want[i])
		}
	}
}
