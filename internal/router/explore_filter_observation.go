package router

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/yusing/mekugi/internal/tokenizer"
)

type exploreObserverKey struct{}

// Invocation-local observations never own a filtering decision or execution.
type exploreObserver struct {
	filtered func(exploreTask, string, string, exploreFilterEvent)
	usage    func(typesafeUsage)
}

type exploreTokenReduction struct {
	Before  int
	After   int
	Saved   int
	Percent float64
	Basis   string
}

// Tokens measure decoded stdout, including the recovery footer, not provider
// input or billed usage. A nil count is unavailable, never a zero saving.
type exploreFilterEvent struct {
	Command      string
	Family       string
	BytesBefore  int
	BytesAfter   int
	Tokens       *exploreTokenReduction `json:",omitempty"`
	LinesBefore  int
	LinesRemoved int
	UnitsBefore  int
	UnitsRemoved int
	ElapsedMS    int64
	JudgeUsage   typesafeUsage
}

func (e exploreFilterEvent) text() string {
	tokens := "~tokens n/a"
	if e.Tokens != nil {
		tokens = fmt.Sprintf("~tokens %s→%s (%+.1f%%)", formatUsageTokens(uint64(e.Tokens.Before)), formatUsageTokens(uint64(e.Tokens.After)), -e.Tokens.Percent)
	}
	return fmt.Sprintf("%s · −%d/%d lines · %.1fs", tokens, e.LinesRemoved, e.LinesBefore, float64(e.ElapsedMS)/1000)
}

func (p *mekugiProxy) exploreContext(ctx context.Context, thread, activityThread string) context.Context {
	return context.WithValue(ctx, exploreObserverKey{}, exploreObserver{
		usage: func(usage typesafeUsage) { p.usage.addTypesafe(thread, usage) },
		filtered: func(task exploreTask, before, after string, event exploreFilterEvent) {
			event.Command = clipExploreText(task.Command, 509) // Reserve three bytes for the ellipsis.
			event.BytesBefore, event.BytesAfter = len(before), len(after)
			if codec, err := tokenizer.New(); err == nil {
				original, beforeErr := codec.Count(before)
				filtered, afterErr := codec.Count(after)
				if beforeErr == nil && afterErr == nil && original > 0 {
					event.Tokens = &exploreTokenReduction{Before: original, After: filtered, Saved: original - filtered,
						Percent: 100 * float64(original-filtered) / float64(original), Basis: codec.GetName()}
				}
			}
			source := fmt.Sprintf("explore-filter\x00%s\x00%x", task.callID, sha256.Sum256([]byte(before)))
			p.activity.collectEvent(activityEvent{thread: activityThread, source: source, kind: "output_filter", callID: task.callID,
				text: event.text(), filter: &event})
		},
	})
}
