package observation

import (
	"time"

	"github.com/yusing/mekugi/contracts/scope"
)

// ItemRef preserves host identity independently of display names and feed order.
type ItemRef struct{ ThreadID, TurnID, ItemID string }

// TextItem contains only authored text and public-summary facts. User text is
// supplied by its Conversation consumer; no raw/private reasoning is carried.
type TextItem struct {
	Ref                  ItemRef
	Type, Text, Status   string
	Summary              []string
	AsyncQuestion, Final bool
}

type TextEvent struct {
	At    time.Time
	Kind  Kind
	Item  TextItem
	Delta string
}

const ReasoningPart Kind = "item/reasoning/summaryPartAdded"

// TextUpdate is a transient projection into an existing feed, not another
// transcript. Sequence is assigned by the feed consumer before terminal pacing.
type TextUpdate struct {
	Ref                       ItemRef
	Replaces                  string
	Kind, Phase, Text, Status string
	Observed, Completed       time.Time
	Thought                   time.Duration
	Live                      bool
	Sequence                  uint64
}

// TextScope carries application-owned correlation stores. A nil Turn admits
// only authoritative completion text, without reviving a closed turn's state.
type TextScope struct{ Thread, Turn *scope.Store }

// Transcript observes admitted text without execution, storage or renderer
// capabilities. Historical projection is deliberately separate from live state.
type Transcript interface {
	// current is false only when the application knows another turn is active.
	TurnCompleted(thread, turn, status string, at time.Time, current bool, stores TextScope) []TextUpdate
	Observe(TextEvent, bool, TextScope) []TextUpdate
	ProviderStarted(thread string, at time.Time, store *scope.Store) []TextUpdate
	OtherItemStarted(thread string, store *scope.Store) []TextUpdate
}
