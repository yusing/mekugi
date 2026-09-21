package router

import (
	"fmt"
	"strings"
	"testing"
)

func BenchmarkMRunRowWindow(b *testing.B) {
	for _, tail := range []bool{false, true} {
		b.Run(fmt.Sprintf("tail=%t", tail), func(b *testing.B) {
			rows := make([]string, 1024)
			for i := range rows {
				rows[i] = strings.Repeat("x", 4095) + "\n"
			}
			capture := mrunCapture{maxLines: len(rows), rows: rows, rowStart: 511, buffer: make([]byte, 132), tail: tail}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				capture.text()
			}
		})
	}
}
