//go:build codex_e2e

package launch

import (
	"context"
	"testing"
	"time"

	"github.com/yusing/mekugi/backends/codex/appserver"
)

func awaitHistoryRPC(t *testing.T, client *appserver.Client, id string) appserver.Message {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	for {
		select {
		case m, ok := <-client.Messages:
			if !ok {
				t.Fatal("host closed during history read")
			}
			if string(m.ID) == id {
				if m.Error != nil {
					t.Fatalf("host history RPC failed: %+v", m.Error)
				}
				return m
			}
		case <-ctx.Done():
			t.Fatal("host history RPC timed out")
		}
	}
}
