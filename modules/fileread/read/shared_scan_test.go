package read

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/libraries/commandargs"
)

type countedSource struct {
	io.Reader
	bytes int
}

func (r *countedSource) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.bytes += n
	return n, err
}

func TestSharedScanValidatesOnceAndKeepsSelectionsIndependent(t *testing.T) {
	selections := []commandargs.ReadSelection{{Path: "source", Start: 3, End: 3, Range: "3:3"}, {Path: "source", Start: 1, End: 2, Range: "1:2"}, {Path: "source", Start: 9, End: 9, Range: "9:9"}}
	for _, invalid := range []bool{false, true} {
		text := "one\r\ntwo\rthree\nfour"
		if invalid {
			text += "\xff"
		}
		source := &countedSource{Reader: strings.NewReader(text)}
		got, err := scanRows(t.Context(), source, selections, commandargs.ReaderOptions{Budget: 1000})
		if err != nil || source.bytes != len(text) || len(got) != 3 {
			t.Fatalf("shared scan: bytes=%d results=%d err=%v", source.bytes, len(got), err)
		}
		if invalid {
			for _, out := range got {
				if out.ExitCode == 0 || out.Stdout != "" {
					t.Fatalf("invalid UTF-8 accepted: %+v", out)
				}
			}
		} else if got[0].Stdout != "three\n" || got[1].Stdout != "one\ntwo\n" || got[2].ExitCode == 0 {
			t.Fatalf("independent selections: %+v", got)
		}
	}
}

func BenchmarkReadRepeatedRanges(b *testing.B) {
	path := filepath.Join(b.TempDir(), "source.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("abcdefghijklmnop\n", 120000)), 0600); err != nil {
		b.Fatal(err)
	}
	for _, count := range []int{1, 16} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			args := []string{path}
			for i := range count {
				args = append(args, fmt.Sprintf("%d:%d", i+1, i+1))
			}
			b.ReportAllocs()
			for b.Loop() {
				out, err := (Service{}).Execute(b.Context(), command.Request{Args: args})
				if err != nil || out.ExitCode != 0 {
					b.Fatalf("read: %+v, %v", out, err)
				}
			}
		})
	}
}
