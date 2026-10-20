package activity

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// Every clipped view counts what it leaves out in one of these forms, and
// the pointer that opens the content underlines exactly the count.
func TestElisionForms(t *testing.T) {
	for _, tc := range []struct {
		elision Elision
		want    string
	}{
		{Elision{Hidden: 1}, "… +1 line"},
		{Elision{Hidden: 4, Form: ElisionSuffix}, "+4 lines"},
		{Elision{Hidden: 3, Unit: "more", Form: ElisionSuffix}, "+3 more"},
		{Elision{Hidden: 1, Form: ElisionEarlier}, "… 1 earlier line"},
		{Elision{Hidden: 12, Form: ElisionLead}, "+12"},
		{Elision{Hidden: 50, Form: ElisionContent}, "(50 lines)"},
		{Elision{}, ""},
	} {
		if got := ansi.Strip(tc.elision.String()); got != tc.want {
			t.Errorf("%+v = %q, want %q", tc.elision, got, tc.want)
		}
	}
	hovered := Elision{Hidden: 2, Hovered: true}.String()
	if !strings.Contains(hovered, "\x1b[4m… +2 lines") || strings.Contains(Elision{Hidden: 2}.String(), "\x1b[4m") {
		t.Fatalf("hover underline = %q", hovered)
	}
}
