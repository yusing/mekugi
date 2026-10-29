package paging

import "testing"

func TestAdmittedArrayValidationAndRawEntries(t *testing.T) {
	for _, invalid := range []string{"null", "{}", "[1] [2]", "[1,]", `[{"a":1,"a":2}]`, "[1, {"} {
		if _, err := Admit("[0]", invalid, "json", "json"); err == nil {
			t.Fatalf("admitted unselected invalid array %q", invalid)
		}
	}
	const text = "[ \n 9007199254740993 , {\"nested\" : [1e400, \"a,]b\"]} , \"\\ud800\" ]\n"
	source, err := Admit(text, "", "json", "")
	if err != nil {
		t.Fatal(err)
	}
	page, err := source.Page(t.Context(), ReadRequest{Position: [2]int{1, 0}}, 1000)
	if err != nil || page.Text != `[{"nested" : [1e400, "a,]b"]},"\ud800"]` || !page.Complete || page.Position != [2]int{3, 0} {
		t.Fatalf("admitted raw continuation: %+v, %v", page, err)
	}
	if _, err := CountArray(`[{"a":1,"a":2}]`, true); err != nil {
		t.Fatalf("legacy validation changed: %v", err)
	}
}
