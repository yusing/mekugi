//go:build journal_e2e

package router

import (
	"bufio"
	"bytes"
	json "encoding/json/v2"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/appserver"
)

func TestJournalHeadlessNativeCodexE2E(t *testing.T) {
	for _, mode := range []string{"off", "slice"} {
		t.Run(mode, func(t *testing.T) { testJournalHeadlessNativeCodex(t, mode) })
	}
}

func testJournalHeadlessNativeCodex(t *testing.T, mode string) {
	ctx, cmd, provider := journalResetCodexFixture(t, false)
	provider.proxy.journalCompaction = mode
	var output bytes.Buffer
	wait, err := startHeadlessAppServer(ctx, cmd, strings.NewReader("Complete the first journal slice."), &output, provider.proxy)
	if err != nil {
		t.Fatal(err)
	}
	if err := wait(); err != nil {
		t.Fatal(err)
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.turns != 2 || provider.compactions != 0 || !provider.continuation || mode == "slice" && !provider.recovered {
		t.Fatalf("headless outcome: turns=%d compactions=%d continued=%t recovered=%t", provider.turns, provider.compactions, provider.continuation, provider.recovered)
	}
	scanner := bufio.NewScanner(&output)
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	var last string
	var compacted, reset bool
	for scanner.Scan() {
		var event appserver.Message
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		last = event.Method
		reset = reset || event.Method == "mekugi/journal/reset"
		compacted = compacted || event.Method == "item/completed" && bytes.Contains(event.Params, []byte(`"contextCompaction"`))
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if last != "mekugi/headless/completed" || compacted != (mode == "slice") || !reset {
		t.Fatalf("missing JSONL lifecycle: last=%q compacted=%t reset=%t", last, compacted, reset)
	}
}
