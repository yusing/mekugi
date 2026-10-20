package outputdedupe

import (
	"fmt"
	"strings"
	"testing"
)

func benchmarkHistory() []string {
	const unitBytes = 64 << 10
	units := make([]string, (4<<20)/unitBytes)
	for source := range units {
		var text strings.Builder
		for row := 0; text.Len() < unitBytes; row++ {
			fmt.Fprintf(&text, "%04d:%06d %s\n", source, row, strings.Repeat("x", 84))
		}
		units[source] = text.String()[:unitBytes]
	}
	return units
}

func benchmarkFragmentedHistory() []string {
	shared := strings.Repeat("b", Threshold) + "\n"
	units := []string{shared}
	for i := range 4096 {
		units = append(units, shared+fmt.Sprintf("%08x\n", i))
	}
	return units
}

func BenchmarkDuplicateOutputProjection(b *testing.B) {
	history := benchmarkHistory()
	b.Run("4MB_history", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(4 << 20)
		for b.Loop() {
			idx := New()
			for source, text := range history {
				idx.Add(source, idx.Project(text))
			}
			idx.Close()
		}
	})
	b.Run("60KB_full_window", func(b *testing.B) {
		idx := New()
		defer idx.Close()
		for source, text := range history {
			idx.Add(source, idx.Project(text))
		}
		text := history[len(history)-1][:60<<10]
		b.ReportAllocs()
		b.SetBytes(int64(len(text)))
		for b.Loop() {
			if len(idx.Project(text).Spans) != 1 {
				b.Fatal("expected a duplicate run")
			}
		}
	})
	b.Run("seedless_window", func(b *testing.B) {
		idx := New()
		defer idx.Close()
		text := strings.Repeat("\n", WindowBytes)
		b.ReportAllocs()
		b.SetBytes(int64(len(text)))
		for b.Loop() {
			idx.Add(1, idx.Project(text))
		}
	})
	b.Run("fragmented_window", func(b *testing.B) {
		history := benchmarkFragmentedHistory()
		b.ReportAllocs()
		for b.Loop() {
			idx := New()
			for source, text := range history {
				idx.Add(source, idx.Project(text))
			}
			idx.Close()
		}
	})
	b.Run("pathological", func(b *testing.B) {
		// Repeated seeds mixed with blank/brace rows stress candidate extension;
		// rows with no seeds must still be cheap to traverse and retain.
		text := strings.Repeat("\n{\n}\nrepeated-seed\n", 4096)
		blank := strings.Repeat("\n{\n}\n", 16384)
		b.ReportAllocs()
		b.SetBytes(int64(len(text)*2 + len(blank)))
		for b.Loop() {
			idx := New()
			idx.Add(1, idx.Project(text))
			idx.Add(2, idx.Project(blank))
			if len(idx.Project(text).Spans) == 0 {
				b.Fatal("expected a duplicate run")
			}
			idx.Close()
		}
	})
}
