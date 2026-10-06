package router

import (
	"cmp"
	"context"
	"strings"
	"sync"
	"time"

	"github.com/yusing/mekugi/capturer"
)

// Thread totals are auxiliary, independent of routing sessions and replay history.
// Existing identities are never evicted: an untracked thread must not later show a
// partial lifetime total as though it were complete.
type threadUsage struct {
	mu            sync.Mutex
	threads       map[string]*threadUsageTotal
	closed        bool
	store         *mekugiReplayStore
	storageFailed map[string]bool
	fresh         map[string]bool // Positive native creation evidence in this router lifetime.
	notice        func(string, error)
	pending       map[string]*threadUsageWrite
	writing       bool
	changed       chan struct{}
	writerCancel  context.CancelFunc
	writerDone    chan struct{}
}

type threadUsageTotal struct {
	roundOutput  providerRoundOutput
	cost         tokenCost
	counts       tokenCounts
	complete     bool
	missingUsage uint64
	roundtrips   uint64
	models       []string
	priorUnknown bool
}

type threadUsageObservation struct {
	started       time.Time
	receivedAt    time.Time
	throughput    capturer.OutputThroughput
	once          sync.Once
	totals        *threadUsage
	conflicted    bool
	model         string
	reasoning     string
	openCodePrice *openCodePrice
	serviceTier   string
	thread        string
}

func newThreadUsage() *threadUsage {
	return &threadUsage{threads: make(map[string]*threadUsageTotal), changed: make(chan struct{})}
}

// Transport identity owns usage accounting, not auxiliary author or ancestry
// metadata. A contradictory explicit thread ID makes lifetime totals incomplete.
func (u *threadUsage) observation(thread, metadataThread, model, serviceTier string) *threadUsageObservation {
	return &threadUsageObservation{totals: u, thread: thread, model: model, serviceTier: serviceTier, conflicted: metadataThread != "" && metadataThread != thread}
}

func (o *threadUsageObservation) observe(counts tokenCounts) {
	if o == nil {
		return
	}
	o.once.Do(func() {
		tier := cmp.Or(counts.ServiceTier, o.serviceTier)
		round := providerRoundOutput{}
		if !o.started.IsZero() {
			round.StartedUnixNano = o.started.UnixNano()
			at := o.receivedAt
			if at.IsZero() {
				at = time.Now()
			}
			round.Throughput = measureOutputThroughput(counts, o.started, at)
		}
		o.throughput = round.Throughput
		o.totals.addRound(o, tier, counts, round)
	})
}

// A forwarded request without terminal usage leaves a permanent accounting gap.
// A later successful response must not revive an apparently complete total.
func (o *threadUsageObservation) finish() {
	o.observe(tokenCounts{Incomplete: true})
}

func (u *threadUsage) addRound(o *threadUsageObservation, serviceTier string, counts tokenCounts, round providerRoundOutput) {
	thread, model, reasoning, conflicted, price := o.thread, o.model, o.reasoning, o.conflicted, o.openCodePrice
	if u == nil || thread == "" || len(thread) > maxCommentaryPublicationBytes {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed {
		return
	}
	delta := newThreadUsageTotal()
	delta.roundOutput = round
	delta.roundtrips = 1 // Forwarded requests count whether or not usage arrived.
	displayModel := usageModelLabel(model, reasoning, serviceTier)
	if displayModel != "" {
		delta.models = []string{displayModel}
	}
	delta.complete = !conflicted
	if counts.Incomplete {
		delta.missingUsage = 1
	} else {
		delta.counts = tokenCounts{
			InputTokens: counts.InputTokens, UncachedInputTokens: counts.UncachedInputTokens,
			CacheWriteTokens: counts.CacheWriteTokens, OutputTokens: counts.OutputTokens,
			ReasoningTokens: counts.ReasoningTokens, Inconsistent: counts.Inconsistent,
		}
		delta.cost = usageTokenCost(model, serviceTier, counts, price)
	}
	u.updateLocked(thread, delta)
}

func usageTokenCost(model, serviceTier string, counts tokenCounts, price *openCodePrice) tokenCost {
	if isOpenCodeModel(model) {
		if price != nil {
			return price.estimate(serviceTier, counts)
		}
		return tokenCost{}
	}
	return estimateTokenCost(model, serviceTier, counts)
}

func usageTotalReport(total *threadUsageTotal) (tokenUsageReport, bool) {
	if total == nil {
		return tokenUsageReport{}, false
	}
	if !total.complete {
		return tokenUsageReport{roundtrips: total.roundtrips, priorUnknown: total.priorUnknown, roundOutput: total.roundOutput}, false
	}
	return tokenUsageReport{tokenCounts: total.counts, cost: total.cost, model: strings.Join(total.models, ", "), missingUsage: total.missingUsage, priorUnknown: total.priorUnknown, roundtrips: total.roundtrips, roundOutput: total.roundOutput}, true
}

func (u *threadUsage) snapshot(thread string) (tokenUsageReport, bool) {
	if u == nil {
		return tokenUsageReport{}, false
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.closed {
		u.loadLocked(thread)
		report, observed := usageTotalReport(u.threads[thread])
		return report, observed
	}
	return tokenUsageReport{}, false
}

func (u *threadUsage) close() {
	if u == nil {
		return
	}
	u.mu.Lock()
	u.closed = true
	cancel, done := u.writerCancel, u.writerDone
	u.mu.Unlock()
	if done != nil {
		ctx, stop := context.WithTimeout(context.Background(), shutdownTimeout)
		_ = u.flush(ctx) // The writer reports any unretained observations through notice.
		stop()
		cancel()
		<-done
	}
	u.mu.Lock()
	clear(u.threads)
	u.mu.Unlock()
}

// Model labels include reasoning and service tier; pricing uses the bare model.
func usageModelLabel(model, reasoning, tier string) string {
	label := strings.TrimSpace(model + " " + reasoning)
	if label != "" && (tier == "fast" || tier == "priority") {
		label += " [fast]"
	}
	return label
}
