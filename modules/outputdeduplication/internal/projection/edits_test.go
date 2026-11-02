package projection

import (
	"encoding/json/jsontext"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	json "encoding/json/v2"

	"github.com/yusing/mekugi/modules/outputdeduplication/internal/matcher"
)

func decodeEdits(t *testing.T, projected, base string) string {
	t.Helper()
	_, rows, ok := strings.Cut(projected, ":\n")
	if !ok {
		t.Fatalf("missing edit header: %q", projected)
	}
	var edits []edit
	for row := range strings.SplitSeq(strings.TrimSuffix(rows, "\n"), "\n") {
		var values []jsontext.Value
		if err := json.Unmarshal([]byte(row), &values); err != nil || len(values) != 3 && len(values) != 2 {
			t.Fatalf("invalid edit %q: %v", row, err)
		}
		var e edit
		if len(values) == 2 {
			var positions []int
			if err := json.Unmarshal(values[0], &positions); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(values[1], &e.text); err != nil {
				t.Fatal(err)
			}
			for _, at := range positions {
				edits = append(edits, edit{start: at, end: at, text: e.text})
			}
			continue
		}
		for i, target := range []any{&e.start, &e.end, &e.text} {
			if err := json.Unmarshal(values[i], target); err != nil {
				t.Fatal(err)
			}
		}
		edits = append(edits, e)
	}
	slices.SortFunc(edits, func(a, b edit) int { return a.start - b.start })
	var text strings.Builder
	at := 0
	for _, e := range edits {
		if e.start < at || e.end < e.start || e.end > len(base) {
			t.Fatalf("invalid base coordinates: %+v", edits)
		}
		text.WriteString(base[at:e.start])
		text.WriteString(e.text)
		at = e.end
	}
	text.WriteString(base[at:])
	return text.String()
}

func TestEditProjectionExactBytes(t *testing.T) {
	for name, pair := range map[string][2]string{
		"insert":        {"", "  \n\t"},
		"delete":        {"remove this row\n", ""},
		"replace":       {"before before", "after before"},
		"unicode":       {"世界é😀", "世語e🥳"},
		"CRLF":          {"row\r\n\t\r\n", "row\n\t\n"},
		"final newline": {"tail\r\n", "tail"},
	} {
		t.Run(name, func(t *testing.T) {
			common := strings.Repeat("valuable unchanged evidence with varied words\n", 25)
			base, body := common+pair[0], common+pair[1]
			var savings Savings
			p := (Service{}).NewPass(&savings)
			defer p.Close()
			p.Text("", base)
			got := p.Text("", body)
			// A cheap single unchanged run may beat edit framing. Exercise the
			// competing representation directly as well as its rendered grammar.
			if !strings.Contains(got.Text, "with byte edits") {
				var ok bool
				got, ok = p.editProjection("", body, strings.Repeat("expensive baseline ", 1000))
				if !ok {
					t.Fatal("no exact edit candidate")
				}
			}
			if rebuilt := decodeEdits(t, got.Text, base); rebuilt != body {
				t.Fatalf("reconstruction = %q, want %q", rebuilt, body)
			}
		})
	}
}

func TestEditPlannerRepeatedUnicode(t *testing.T) {
	rng := rand.New(rand.NewPCG(359, 1))
	alphabet := []rune("ab \t\r\n世界😀")
	for range 500 {
		a := make([]rune, rng.IntN(150))
		for i := range a {
			a[i] = alphabet[rng.IntN(len(alphabet))]
		}
		b := append([]rune(nil), a...)
		for range 5 {
			at := rng.IntN(len(b) + 1)
			end := min(at+rng.IntN(5), len(b))
			b = append(append(append([]rune(nil), b[:at]...), alphabet[rng.IntN(len(alphabet))]), b[end:]...)
		}
		base, body := string(a), string(b)
		work := editWork
		edits, ok := planEdits(base, body, &work)
		if !ok {
			t.Fatal("small edit exhausted budget")
		}
		if rebuilt, _, _ := reconstruct(base, body, edits); rebuilt != body {
			t.Fatalf("%q -> %q: edits %+v rebuilt %q", base, body, edits, rebuilt)
		}
	}
}

func nearBodies() (string, string) {
	var base, body strings.Builder
	for i := range 45 {
		row := fmt.Sprintf("requirement %02d: preserve original evidence and direct references %s\n", i, strings.Repeat("valuable detail ", 4))
		base.WriteString(row)
		if i == 1 {
			row = strings.Replace(row, "original", "unchanged", 1)
		}
		body.WriteString(row)
		if i%5 == 0 {
			body.WriteString("    \n")
		}
	}
	return base.String(), body.String()
}

func TestEditProjectionSelectionAndDirectSources(t *testing.T) {
	base, body := nearBodies()
	var savings Savings
	p := (Service{}).NewPass(&savings)
	defer p.Close()
	source := p.Text("", base).Text
	got := p.Text("", body)
	if !strings.HasPrefix(got.Text, "See O1 with byte edits") || decodeEdits(t, got.Text, base) != body {
		candidate, ok := p.editProjection("", body, strings.Repeat("baseline ", 1000))
		t.Fatalf("near match did not choose direct edits: baseline %d candidate %d ok=%v %q", savings.count(got.Text), savings.count(candidate.Text), ok, candidate.Text)
	}
	if got.Occurrences != 1 || got.RepeatedLines == 0 || got.RepeatedBytes >= len(body) {
		t.Fatalf("receipt = %+v", got)
	}
	// Edits are never sources. Every repeat still uses the original O1.
	if repeat := p.Text("", body); repeat.Text != got.Text || p.sources != 1 {
		t.Fatalf("projected body became a source: %q", repeat.Text)
	}
	for _, baseline := range []string{got.Text, "See O1\n"} {
		if _, ok := p.editProjection("", body, baseline); ok {
			t.Fatal("equal or cheaper baseline lost")
		}
	}
	fresh := (Service{}).NewPass(&Savings{})
	defer fresh.Close()
	if earlier := fresh.Text("", base).Text; earlier != source {
		t.Fatal("request extension changed source")
	}
	if isolated := fresh.Text("", body).Text; isolated != got.Text {
		t.Fatal("fresh pass changed projection")
	}
	p.Text("", strings.Repeat("\n", matcher.WindowBytes))
	if _, ok := p.editProjection("", body, strings.Repeat("baseline ", 1000)); ok {
		t.Fatal("evicted base used")
	}
	other := (Service{}).NewPass(&Savings{})
	defer other.Close()
	if strings.Contains(other.Text("", body).Text, "with byte edits") {
		t.Fatal("out-of-request source used")
	}
}

func TestEditPlanningWorkBound(t *testing.T) {
	for _, pair := range [][2]string{
		{strings.Repeat("repeated row\n", 15000), strings.Repeat("repeated row\n\n", 15000)},
		{strings.Repeat("x", 120000) + "before" + strings.Repeat("x", 120000), strings.Repeat("x", 120000) + "after" + strings.Repeat("x", 120000)},
	} {
		work := 100
		if _, ok := planEdits(pair[0], pair[1], &work); ok || work > 0 {
			t.Fatal("planner ignored exhausted budget")
		}
		work = editWork
		_, _ = planEdits(pair[0], pair[1], &work)
		if used := editWork - work; used > editWork+editDistance*2+1 {
			t.Fatalf("unbounded planner work: %d", used)
		}
	}
}

func TestEditLongSingleLineReceipt(t *testing.T) {
	base := strings.Repeat("valuable single line evidence ", 3000)
	body := base[:30000] + "世界\t" + base[30000:]
	p := (Service{}).NewPass(&Savings{})
	defer p.Close()
	p.Text("", base)
	got := p.Text("", body)
	if !strings.HasPrefix(got.Text, "See O1 with byte edits") || decodeEdits(t, got.Text, base) != body ||
		got.RepeatedLines != 0 || got.RepeatedBytes != len(base) {
		t.Fatalf("single-line edits or receipt = %+v", got)
	}
}

func BenchmarkSingleBaseEdits(b *testing.B) {
	base, near := nearBodies()
	for name, bodies := range map[string][]string{
		"many_near_matches": {base, near, near, near, near, near, near, near},
		"repetitive":        {strings.Repeat("repeated evidence\n", 10000), strings.Repeat("repeated evidence\n\n", 10000)},
		"long_single_line":  {strings.Repeat("valuable evidence ", 12000), strings.Repeat("valuable evidence ", 6000) + "changed " + strings.Repeat("valuable evidence ", 6000)},
	} {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				p := (Service{}).NewPass(&Savings{})
				for _, body := range bodies {
					p.Text("", body)
				}
				p.Close()
			}
		})
	}
}
