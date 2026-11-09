package compact

import (
	"context"
	"strings"
	"testing"

	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func TestMessageSourceRunnerSharesNestedBounds(t *testing.T) {
	var limits []int
	snapshot, err := ReadSourcesWithRunner(t.Context(), `{"sources":[{"path":"root"}]}`, t.TempDir(), func(_ context.Context, script string, limit int) (CapturedSource, error) {
		limits = append(limits, limit)
		body := "LEAF_BYTES"
		if len(limits) == 1 {
			body = "ROOT_BYTES @nested"
		}
		return CapturedSource{Command: script, Stdout: body}, nil
	})
	if err != nil || len(limits) != 2 || limits[0] != history.MaxRecordBytes || limits[1] >= limits[0] || !strings.Contains(snapshot.Text(), "LEAF_BYTES") {
		t.Fatalf("shared runner bounds: %v %s %v", limits, snapshot.Text(), err)
	}
}

func TestMessageSourceRunnerRejectsCaptureOverflow(t *testing.T) {
	reads := 0
	_, err := ReadSourcesWithRunner(t.Context(), `{"sources":[{"path":"huge"},{"path":"later"}]}`, t.TempDir(), func(_ context.Context, _ string, limit int) (CapturedSource, error) {
		reads++
		return CapturedSource{Stdout: strings.Repeat("x", limit+1)}, nil
	})
	if err == nil || reads != 1 {
		t.Fatalf("overflow continued or looked complete: %d %v", reads, err)
	}
}
