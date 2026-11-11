package accounting

// Source: internal/router/thread_usage.go@47bcdd8e provider accounting.

import (
	"cmp"
	"context"
	"strings"
	"sync"
	"time"

	"github.com/yusing/mekugi/contracts/inference"

	"github.com/yusing/mekugi/libraries/persistence"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

// Thread totals are auxiliary, independent of routing sessions and replay history.
// Existing identities are never evicted: an untracked thread must not later show a
// partial lifetime total as though it were complete.
type Service struct {
	mu            sync.Mutex
	threads       map[string]*threadUsageTotal
	closed        bool
	store         *history.Store
	writes        *persistence.Counter
	limits        history.Limits
	writerCtx     context.Context
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
	roundOutput  RoundOutput
	cost         Cost
	counts       inference.Usage
	complete     bool
	missingUsage uint64
	roundtrips   uint64
	models       []string
	priorUnknown bool
}

type Observation struct {
	started     time.Time
	ReceivedAt  time.Time
	Throughput  inference.OutputThroughput
	once        sync.Once
	totals      *Service
	conflicted  bool
	model       string
	Reasoning   string
	Price       func(string, inference.Usage) Cost
	serviceTier string
	thread      string
}

func New() *Service {
	return &Service{threads: make(map[string]*threadUsageTotal), changed: make(chan struct{})}
}

// Transport identity owns usage accounting, not auxiliary author or ancestry
// metadata. A contradictory explicit thread ID makes lifetime totals incomplete.
func (u *Service) Observation(thread, metadataThread, model, serviceTier string) *Observation {
	if u == nil {
		return nil
	}
	return &Observation{totals: u, thread: thread, model: model, serviceTier: serviceTier, conflicted: metadataThread != "" && metadataThread != thread}
}

func (o *Observation) Observe(counts inference.Usage) {
	if o == nil {
		return
	}
	o.once.Do(func() {
		tier := cmp.Or(counts.ServiceTier, o.serviceTier)
		round := RoundOutput{}
		if !o.started.IsZero() {
			round.StartedUnixNano = o.started.UnixNano()
			at := o.ReceivedAt
			if at.IsZero() {
				at = time.Now()
			}
			round.Throughput = MeasureThroughput(counts, o.started, at)
		}
		o.Throughput = round.Throughput
		o.totals.addRound(o, tier, counts, round)
	})
}

// A forwarded request without terminal usage leaves a permanent accounting gap.
// A later successful response must not revive an apparently complete total.
func (o *Observation) Finish() {
	o.Observe(inference.Usage{Incomplete: true})
}

func (u *Service) addRound(o *Observation, serviceTier string, counts inference.Usage, round RoundOutput) {
	thread, model, reasoning, conflicted, price := o.thread, o.model, o.Reasoning, o.conflicted, o.Price
	if u == nil || thread == "" || len(thread) > maxThreadIdentityBytes {
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
		delta.counts = inference.Usage{
			InputTokens: counts.InputTokens, UncachedInputTokens: counts.UncachedInputTokens,
			CacheWriteTokens: counts.CacheWriteTokens, OutputTokens: counts.OutputTokens,
			ReasoningTokens: counts.ReasoningTokens, Inconsistent: counts.Inconsistent,
		}
		if price != nil {
			delta.cost = price(serviceTier, counts)
		} else {
			delta.cost = estimateReferenceCost(model, serviceTier, counts)
		}
	}
	u.updateLocked(thread, delta)
}

func usageTotalReport(total *threadUsageTotal) (Report, bool) {
	if total == nil {
		return Report{}, false
	}
	if !total.complete {
		return Report{Roundtrips: total.roundtrips, PriorUnknown: total.priorUnknown, RoundOutput: total.roundOutput}, false
	}
	return Report{Usage: total.counts, Cost: total.cost, Model: strings.Join(total.models, ", "), MissingUsage: total.missingUsage, PriorUnknown: total.priorUnknown, Roundtrips: total.roundtrips, RoundOutput: total.roundOutput}, true
}

func (u *Service) Snapshot(thread string) (Report, bool) {
	if u == nil {
		return Report{}, false
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.closed {
		u.loadLocked(thread)
		report, observed := usageTotalReport(u.threads[thread])
		return report, observed
	}
	return Report{}, false
}

func (u *Service) Close() {
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
	}
	if cancel != nil {
		cancel()
	}
	if done != nil {
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

// Start establishes the invocation lifetime; publication workers start on demand.
func (u *Service) Start(ctx context.Context) error {
	u.writerCtx, u.writerCancel = context.WithCancel(context.WithoutCancel(ctx))
	return nil
}

// Configure attaches the selected publication boundary before observations arrive.
func (u *Service) Configure(store *history.Store, writes *persistence.Counter, limits history.Limits, notice func(string, error)) {
	if u == nil {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.store = store
	u.writes, u.limits, u.notice = writes, limits, notice
}

const maxThreadIdentityBytes = 16 << 10
const shutdownTimeout = 5 * time.Second
