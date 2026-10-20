package livediff

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestSafePooledStorageDoesNotEscape(t *testing.T) {
	for _, size := range []int{32, 511, 512, 513, 4096, 2 << 20} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			plain := strings.Repeat("x", size)
			input := plain + "\t界 é\x1b[31mred\x1b[0m\x1b[2J\x1b]52;c;ignored\a\n"
			want := plain + "    界 éred\n"
			retained := Safe(input, false)
			if retained != want {
				t.Fatal("sanitization changed")
			}
			var workers sync.WaitGroup
			for range 4 {
				workers.Go(func() {
					for range 4 {
						got := Safe(strings.Repeat("y", size)+"\t\x1b[31mz\x1b[0m", true)
						if got != strings.Repeat("y", size)+"    \x1b[31mz\x1b[0m" {
							t.Error("concurrent sanitization borrowed another result")
						}
					}
				})
			}
			workers.Wait()
			if retained != want {
				t.Fatal("returned string aliased recycled storage")
			}
		})
	}
}

func BenchmarkSafeTerminalText(b *testing.B) {
	for _, size := range []int{32, 512, 4096, 16384} {
		for _, colors := range []bool{false, true} {
			b.Run(fmt.Sprintf("bytes-%d/colors-%t", size, colors), func(b *testing.B) {
				text := strings.Repeat("source\t界 é \x1b[31mred\x1b[0m\n", size/32+1)
				want := Safe(text, colors)
				b.ReportAllocs()
				b.SetBytes(int64(len(text)))
				for b.Loop() {
					if Safe(text, colors) != want {
						b.Fatal("sanitization changed")
					}
				}
			})
		}
	}
}
