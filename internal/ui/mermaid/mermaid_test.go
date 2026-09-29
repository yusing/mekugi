package mermaid

import (
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// Fixture and cases ported from codex-rs/mermaid/src/tests.rs:23:143
// at rust-v0.159.0 (687a119f0fcaace47e1f1abcc77cec6c813fd6da).
func TestUpstreamDirections(t *testing.T) {
	want, err := os.ReadFile("testdata/directions.txt")
	if err != nil {
		t.Fatal(err)
	}
	var cases []string
	for _, direction := range []string{"TD", "BT", "LR", "RL"} {
		source := fmt.Sprintf("flowchart %s; A([请求]) -- go --> B[Work] & C{Done?}; B -. no .-> C; C <--> A; B --- A; C -.- B; A <-.-> B", direction)
		rows, ok := Render(source, 100)
		if !ok {
			t.Fatal(direction)
		}
		width := 0
		for _, row := range rows {
			width = max(width, ansi.StringWidth(row))
		}
		exact, ok := Render(source, width)
		if !ok || !reflect.DeepEqual(rows, exact) {
			t.Fatal("exact width")
		}
		if _, ok := Render(source, width-1); ok {
			t.Fatal("accepted too narrow")
		}
		cases = append(cases, direction+"\n"+strings.Join(rows, "\n"))
	}
	if got := strings.Join(cases, "\n\n") + "\n"; got != string(want) {
		t.Fatalf("upstream snapshot mismatch:\n%s", got)
	}
}
func TestEquivalentForms(t *testing.T) {
	for _, pair := range [][2]string{
		{"A -- Yes --> B --> C", "A -->|Yes| B --> C"},
		{`A -- "Yes & continue" --> B`, `A -->|"Yes & continue"| B`},
		{"A -. retry .-> B", "A -.->|retry| B"},
		{"A[Input & config] & B -- send --> C & D -.-> E", "A[Input & config];B;C;D;E;A -->|send| C;A -->|send| D;B -->|send| C;B -->|send| D;C -.-> E;D -.-> E"},
		{"A --> B & B", "A --> B; A --> B"},
		{`A["Review & confirm;"] -->|"Yes & continue"| B{"Ready?"}`, `A[Review & confirm;] -->|Yes & continue| B{Ready?}`},
	} {
		a, ok := parse("graph;" + pair[0])
		b, ok2 := parse("flowchart TD;" + pair[1])
		if !ok || !ok2 || !reflect.DeepEqual(a, b) {
			t.Fatalf("not equivalent: %q", pair)
		}
	}
	g, ok := parse(`graph TD;A["chimpansen hoppar ()[]"] -->|"x | y; z"| B{"x < 3?"};`)
	if !ok || g.nodes[0].label != "chimpansen hoppar ()[]" || g.nodes[1].label != "x < 3?" || g.edges[0].label != "x | y; z" {
		t.Fatalf("punctuation: %+v", g)
	}
}
func TestFallback(t *testing.T) {
	for _, body := range []string{"A -->", "A &", "A --> & B", "A --o B", "A---oB", "A-.-xB", "A === B", "A -- label --- B", "A <-->|| B", "subgraph S;A;end", "A[(Database)]", "A[[nested]]", "A[one];A[two]", `A["<b>HTML</b>"]`, "A[&amp;]", "A[&#38;]", "A[#semi;]", "A[\x1b]", "A[e\u0301]", "A[\"unfinished]", "A[\"`markdown`\"]", "A[" + strings.Repeat("x", 41) + "]", strings.Repeat("A & ", 24) + "A", "A & B & C & D & E --> F & G & H & I & J"} {
		if rows, ok := Render("flowchart TD;"+body, 1000); ok || rows != nil {
			t.Fatalf("accepted %q", body)
		}
	}
	for _, src := range []string{"sequenceDiagram\nA->>B: hello", "%%{init: x}\nflowchart;A", strings.Repeat(" ", maxSource+1), "flowchart", "graph ZZ;A"} {
		if _, ok := Render(src, 100); ok {
			t.Fatalf("accepted %q", src)
		}
	}
}
func TestLimitsAndLoops(t *testing.T) {
	source := "graph TD;" + strings.Repeat("A --> A;", 24)
	if _, ok := Render(source, 100); !ok {
		t.Fatal("24 edges rejected")
	}
	if _, ok := Render(source+"A --> A", 100); ok {
		t.Fatal("25 edges accepted")
	}
	source = "graph;"
	for i := range 16 {
		source += fmt.Sprintf("N%d;", i)
	}
	if _, ok := Render(source, 100); !ok {
		t.Fatal("16 nodes rejected")
	}
	if _, ok := Render(source+"extra", 100); ok {
		t.Fatal("17 nodes accepted")
	}
}
func FuzzRender(f *testing.F) {
	for _, s := range []string{"graph TD;A & B --> C & D", "graph RL;A -. retry .-> A", `graph;A["a;b"] -->|"x|y"| B`} {
		f.Add(s, 80)
	}
	f.Fuzz(func(t *testing.T, source string, width int) {
		rows, ok := Render(source, width)
		if !ok && rows != nil {
			t.Fatal("partial diagram")
		}
		cells := 0
		for _, row := range rows {
			w := ansi.StringWidth(row)
			if w > width {
				t.Fatal("overflow")
			}
			cells += w
		}
		if cells > maxCells {
			t.Fatal("unbounded canvas")
		}
	})
}

// Source: codex-rs/mermaid/src/tests.rs:308:410@[687a119f0fcaace47e1f1abcc77cec6c813fd6da] reconstruct_every_edge_from_rendered_paths
func TestReconstructEveryEdge(t *testing.T) {
	for mask := range 512 {
		for _, direction := range []string{"TD", "BT", "LR", "RL"} {
			source := "graph " + direction + ";A;B;C;"
			var want []string
			for from := range 3 {
				for to := range 3 {
					if mask&(1<<(from*3+to)) != 0 {
						a, b := rune('A'+from), rune('A'+to)
						source += fmt.Sprintf("%c-->%c;", a, b)
						want = append(want, string([]rune{a, b}))
					}
				}
			}
			lines, ok := Render(source, 100)
			if !ok {
				t.Fatal(source)
			}
			rows := make([][]rune, len(lines))
			width := 0
			for i, line := range lines {
				rows[i] = []rune(line)
				width = max(width, len(rows[i]))
			}
			if direction == "LR" || direction == "RL" {
				transposed := make([][]rune, width)
				for x := range width {
					for _, row := range rows {
						c := ' '
						if x < len(row) {
							c = transpose(row[x])
						}
						transposed[x] = append(transposed[x], c)
					}
				}
				rows = transposed
			}
			owners := make([]rune, len(rows))
			order := ""
			for top, row := range rows {
				if len(row) == 0 || row[0] != '┌' {
					continue
				}
				bottom := top + 1
				for bottom < len(rows) && (len(rows[bottom]) == 0 || rows[bottom][0] != '└') {
					bottom++
				}
				if bottom == len(rows) {
					t.Fatal("missing border", source)
				}
				var owner rune
				for _, row := range rows[top : bottom+1] {
					for _, c := range row {
						if c == 'A' || c == 'B' || c == 'C' {
							owner = c
						}
					}
				}
				for y := top; y <= bottom; y++ {
					owners[y] = owner
				}
				order += string(owner)
			}
			expectedOrder := "ABC"
			if direction == "BT" || direction == "RL" {
				expectedOrder = "CBA"
			}
			if order != expectedOrder {
				t.Fatal("node order", source, order)
			}
			var got []string
			for y, row := range rows {
				for port := 0; port+1 < len(row); port++ {
					if row[port] != '├' || row[port+1] != '─' {
						continue
					}
					lane := port + 1
					for lane < len(row) && row[lane] != '┐' && row[lane] != '┘' {
						lane++
					}
					if lane == len(row) {
						t.Fatal("missing lane", source)
					}
					step := 1
					if row[lane] == '┘' {
						step = -1
					}
					target := y
					for {
						target += step
						if target < 0 || target >= len(rows) || lane >= len(rows[target]) {
							t.Fatal("broken lane", source)
						}
						c := rows[target][lane]
						if c == '┐' || c == '┘' {
							break
						}
						if c != '│' && c != '╪' {
							t.Fatal("broken crossing", source)
						}
					}
					if rows[target][port] != '├' || rows[target][port+1] != '◄' {
						t.Fatal("wrong endpoint", source)
					}
					for _, c := range rows[target][port+2 : lane] {
						if c != '─' && c != '╪' {
							t.Fatal("broken endpoint route", source)
						}
					}
					got = append(got, string([]rune{owners[y], owners[target]}))
				}
			}
			slices.Sort(got)
			if !slices.Equal(got, want) {
				t.Fatalf("%s: got %v, want %v", source, got, want)
			}
		}
	}
}
