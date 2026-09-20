package router

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/yusing/mekugi"
)

// Large diffs remain visible as an explicitly bounded window of the actual
// unified diff. Do not invent hunk geometry or discard the entire preview.
func boundLiveDiffPreview(preview liveDiffPreview) liveDiffPreview {
	const limit = 48 << 10
	if len(mustMarshalJSON(preview)) <= limit {
		return preview
	}
	if len(preview.Files) != 0 {
		var tail string
		for i := len(preview.Files) - 1; i >= 0 && len(tail) < 16<<10; i-- {
			diff := preview.Files[i].UnifiedDiff()
			if len(diff) > 16<<10 {
				diff = diff[len(diff)-(16<<10):]
			}
			tail = diff + tail
		}
		preview.Input = tail
		preview.Files, preview.Syntax = nil, nil
		preview.DiffText, preview.Truncated = true, true
	}
	for preview.Input != "" && len(mustMarshalJSON(preview)) > limit {
		cut := max(1, len(preview.Input)/2)
		for cut < len(preview.Input) && !utf8.RuneStart(preview.Input[cut]) {
			cut++
		}
		preview.Input = preview.Input[cut:]
		preview.Syntax = liveDiffClipSyntax(preview.Syntax, cut)
		preview.Truncated = true
	}
	// A suffix may have started inside a UTF-8 rune before the size loop.
	for preview.Input != "" && !utf8.RuneStart(preview.Input[0]) {
		preview.Input = preview.Input[1:]
	}
	if preview.Truncated {
		preview.Input = strings.Clone(preview.Input)
	}
	return preview
}

func (s *httpShellCommentarySink) PublishPreWrite(ctx context.Context, preview liveDiffPreview) error {
	_, err := s.send(ctx, map[string]any{"preview": preview})
	return err
}

// Attach to the execution's existing evaluation, after shell expansion and
// recovery. Completion carries the same snapshot so coalescing cannot erase a
// fast pre-write frame before the viewer ever samples it.
func shellPreWriteObserver(ctx context.Context, sink shellCommentarySink, callID string) (context.Context, func()) {
	publisher, ok := sink.(interface {
		PublishPreWrite(context.Context, liveDiffPreview) error
	})
	if !ok {
		return ctx, func() {}
	}
	var preview *liveDiffPreview
	ctx = mekugi.WithPreWriteObserver(ctx, func(files []mekugi.ReviewFile) {
		snapshot := boundLiveDiffPreview(liveDiffPreview{
			ID: callID, Files: files, Evaluated: true, Status: "PRE-WRITE DIFF",
		})
		if len(files) == 0 {
			snapshot.Status += " · no changes"
		}
		preview = &snapshot
		_ = publisher.PublishPreWrite(ctx, snapshot)
	})
	return ctx, func() {
		if preview != nil {
			preview.Complete = true
			// Cancellation stops the edit, not the best-effort removal of its
			// auxiliary active state. The HTTP client still bounds this request.
			_ = publisher.PublishPreWrite(context.WithoutCancel(ctx), *preview)
		}
	}
}
