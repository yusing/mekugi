package main

import (
	"context"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/composition"
)

func testComposition(t *testing.T) *composition.Runtime {
	t.Helper()
	runtime, err := composition.Frontends(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return runtime
}
