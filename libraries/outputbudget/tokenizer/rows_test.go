package tokenizer

import (
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"
)

func TestSelectRowsMatchesGreedyCounts(t *testing.T) {
	c, err := New()
	if err != nil {
		t.Fatal(err)
	}
	random := rand.New(rand.NewPCG(7, 11))
	values := []string{strings.Repeat("\n", 200), strings.Repeat(" \t\n", 100), "!!!\n/\n//\n\nhello\n"}
	atoms := []string{"hello", "世界🙂", " ", "\t", "\n", "/", "!!!", "'s", "UPPER", "123", "e\u0301", "\r"}
	for range 100 {
		var value strings.Builder
		for range 50 {
			value.WriteString(atoms[random.IntN(len(atoms))])
		}
		value.WriteByte('\n')
		values = append(values, value.String())
	}
	for index, value := range values {
		rows := strings.SplitAfter(value, "\n")
		rows = rows[:len(rows)-1]
		for _, tail := range []bool{false, true} {
			for limit := 1; limit <= 40; limit++ {
				want := ""
				for step := range len(rows) {
					candidate := want + rows[step]
					if tail {
						candidate = rows[len(rows)-step-1] + want
					}
					n, err := c.Count(candidate)
					if err != nil {
						t.Fatal(err)
					}
					if n > limit {
						break
					}
					want = candidate
				}
				got, err := c.SelectRows(value, limit, tail)
				if err != nil || got != want {
					t.Fatalf("value %d tail=%t limit=%d: got %q, want %q: %v; input %q", index, tail, limit, got, want, err, value)
				}
			}
		}
	}
}

func TestSelectRowsRejectsInvalidArguments(t *testing.T) {
	c, err := New()
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"", "row\n", "unterminated"} {
		if _, err := c.SelectRows(value, 0, false); err == nil {
			t.Fatalf("zero budget accepted for %q", value)
		}
	}
	if _, err := c.SelectRows("unterminated", 20, false); err == nil {
		t.Fatal("unterminated row accepted")
	}
}

func TestNewlineCountsAndGreedyBoundaries(t *testing.T) {
	c, err := New()
	if err != nil {
		t.Fatal(err)
	}
	lengths := []int{1023, 1024, 4095, 4096, 4097, 8192, 65536}
	for n := range 65 {
		lengths = append(lengths, n)
	}
	for _, n := range lengths {
		want, err := c.Count(strings.Repeat("\n", n))
		if err != nil || newlineTokens(n) != want {
			t.Fatalf("LF count %d: %d, want %d: %v", n, newlineTokens(n), want, err)
		}
	}
	for _, tail := range []bool{false, true} {
		allocations := func(n int) float64 {
			value := strings.Repeat("\n", n)
			return testing.AllocsPerRun(3, func() {
				got, err := c.SelectRows(value, n/2, tail)
				if err != nil || got != value {
					t.Fatal("blank rows lost", err)
				}
			})
		}
		if small, large := allocations(1000), allocations(8000); large > small+8 {
			t.Fatalf("allocations grew per LF boundary: %.0f -> %.0f", small, large)
		}
	}
}

func BenchmarkSelectBlankRows(b *testing.B) {
	c, err := New()
	if err != nil {
		b.Fatal(err)
	}
	for _, rows := range []int{8000, 64000} {
		for _, tail := range []bool{false, true} {
			b.Run(strconv.Itoa(rows)+"/tail="+strconv.FormatBool(tail), func(b *testing.B) {
				value := strings.Repeat("\n", rows)
				b.ReportAllocs()
				for b.Loop() {
					got, err := c.SelectRows(value, rows/2, tail)
					if err != nil || got != value {
						b.Fatal("blank rows lost", err)
					}
				}
			})
		}
	}
}

func BenchmarkSelectRows(b *testing.B) {
	c, err := New()
	if err != nil {
		b.Fatal(err)
	}
	for _, rows := range []int{1000, 2000, 3000} {
		value := strings.Repeat("word\n", rows)
		b.Run(strconv.Itoa(rows), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(value)))
			for b.Loop() {
				if _, err := c.SelectRows(value, 6000, false); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
