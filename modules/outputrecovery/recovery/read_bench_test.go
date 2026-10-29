package recovery

import (
	"fmt"
	"strings"
	"testing"

	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func BenchmarkReadJSON(b *testing.B) {
	for _, size := range []int{1 << 20, 8 << 20} {
		b.Run(fmt.Sprintf("%dMiB", size>>20), func(b *testing.B) {
			histories, err := history.NewService(RecordKinds())
			if err != nil {
				b.Fatal(err)
			}
			store, err := histories.Open(b.Context(), b.TempDir())
			if err != nil {
				b.Fatal(err)
			}
			service, err := New(histories, nil)
			if err != nil {
				b.Fatal(err)
			}
			session, err := service.AttachStore(store, "", "", DefaultLimits(), nil)
			if err != nil {
				b.Fatal(err)
			}
			text := "[" + strings.Repeat(`{"x":1},`, size/8) + "0]"
			id, err := session.Publish(b.Context(), command.OmittedOutput{Stdout: text, StdoutKind: "json"}, 0)
			if err != nil {
				b.Fatal(err)
			}
			request := command.Request{Scope: session.scope, Args: []string{id, "--max-tokens", "128"}}
			// Warm tokenizer and continuation publication outside the measured reads.
			if output, err := service.Execute(b.Context(), request); err != nil || output.Stdout == "" || !strings.Contains(output.Stderr, "next_call") {
				b.Fatalf("read: %+v, %v", output, err)
			}
			b.ReportAllocs()
			b.SetBytes(int64(len(text)))
			b.ResetTimer()
			for b.Loop() {
				if output, err := service.Execute(b.Context(), request); err != nil || output.Stdout == "" || !strings.Contains(output.Stderr, "next_call") {
					b.Fatalf("read: %+v, %v", output, err)
				}
			}
		})
	}
}
