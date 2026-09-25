package router

import (
	"cmp"
	"slices"
	"strings"
	"sync"
)

// Thread totals are auxiliary, independent of routing sessions and replay history.
// Existing identities are never evicted: an untracked thread must not later show a
// partial lifetime total as though it were complete.
type threadUsage struct {
	mu       sync.Mutex
	threads  map[string]*threadUsageTotal
	typesafe map[string]typesafeUsage
	turns    map[usageTurnKey]*threadUsageTotal
	closed   bool
}

type usageTurnKey struct{ thread, turn string }

const maxTrackedUsageTurns = 4096

type threadUsageTotal struct {
	mentorFrom     string
	lastModel      string
	mentorRevision uint64
	cost           tokenCost
	counts         tokenCounts
	complete       bool
	missingUsage   uint64
	models         []string
}

type threadUsageObservation struct {
	once          sync.Once
	totals        *threadUsage
	conflicted    bool
	model         string
	reasoning     string
	openCodePrice *openCodePrice
	serviceTier   string
	thread        string
	turnID        string
}

func newThreadUsage() *threadUsage {
	return &threadUsage{threads: make(map[string]*threadUsageTotal)}
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
		o.totals.add(o.thread, o.model, o.reasoning, tier, counts, o.conflicted, o.openCodePrice)
		o.totals.addTurn(o.thread, o.turnID, o.model, o.reasoning, tier, counts, o.conflicted, o.openCodePrice)
	})
}

// A forwarded request without terminal usage leaves a permanent accounting gap.
// A later successful response must not revive an apparently complete total.
func (o *threadUsageObservation) finish() {
	o.observe(tokenCounts{Incomplete: true})
}

func (u *threadUsage) add(thread, model, reasoning, serviceTier string, counts tokenCounts, conflicted bool, price *openCodePrice) {
	if u == nil || thread == "" || len(thread) > maxCommentaryPublicationBytes {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed {
		return
	}
	total := u.threads[thread]
	if total == nil {
		total = &threadUsageTotal{complete: true, cost: tokenCost{known: true}}
		u.threads[thread] = total
	}
	addThreadUsageTotal(total, model, reasoning, serviceTier, counts, conflicted, price)
}

func addThreadUsageTotal(total *threadUsageTotal, model, reasoning, serviceTier string, counts tokenCounts, conflicted bool, price *openCodePrice) {
	total.lastModel = model
	displayModel := usageModelLabel(model, reasoning, serviceTier)
	if displayModel != "" && !slices.Contains(total.models, displayModel) {
		total.models = append(total.models, displayModel)
	}
	if conflicted {
		total.complete = false
	}
	if !total.complete {
		return
	}
	if counts.Incomplete {
		if total.missingUsage == ^uint64(0) {
			total.complete = false
		} else {
			total.missingUsage++
		}
		return
	}
	sum := total.counts
	for _, pair := range []struct {
		dst *uint64
		add uint64
	}{
		{&sum.InputTokens, counts.InputTokens},
		{&sum.UncachedInputTokens, counts.UncachedInputTokens},
		{&sum.CacheWriteTokens, counts.CacheWriteTokens},
		{&sum.OutputTokens, counts.OutputTokens},
		{&sum.ReasoningTokens, counts.ReasoningTokens},
	} {
		if ^uint64(0)-*pair.dst < pair.add {
			total.complete = false
			return
		}
		*pair.dst += pair.add
	}
	sum.Inconsistent = sum.Inconsistent || counts.Inconsistent
	total.cost.add(usageTokenCost(model, serviceTier, counts, price))
	total.counts = sum
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

func (u *threadUsage) observationForTurn(thread, metadataThread, turnID, model, tier string) *threadUsageObservation {
	observation := u.observation(thread, metadataThread, model, tier)
	observation.turnID = turnID
	return observation
}

func (u *threadUsage) addTurn(thread, turn, model, reasoning, tier string, counts tokenCounts, conflicted bool, price *openCodePrice) {
	if u == nil || thread == "" || turn == "" || len(thread) > maxCommentaryPublicationBytes || len(turn) > maxCommentaryPublicationBytes {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed {
		return
	}
	if u.turns == nil {
		u.turns = make(map[usageTurnKey]*threadUsageTotal)
	}
	key := usageTurnKey{thread, turn}
	total := u.turns[key]
	if total == nil {
		// Never evict a tracked turn and later revive it with a partial total.
		if len(u.turns) >= maxTrackedUsageTurns {
			return
		}
		total = &threadUsageTotal{complete: true, cost: tokenCost{known: true}}
		u.turns[key] = total
	}
	addThreadUsageTotal(total, model, reasoning, tier, counts, conflicted, price)
}

func usageTotalReport(total *threadUsageTotal) (tokenUsageReport, bool) {
	if total == nil || !total.complete {
		return tokenUsageReport{}, false
	}
	return tokenUsageReport{tokenCounts: total.counts, cost: total.cost, model: strings.Join(total.models, ", "), missingUsage: total.missingUsage}, true
}

func (u *threadUsage) turnSnapshot(thread, turn string) (tokenUsageReport, bool) {
	if u == nil || turn == "" {
		return tokenUsageReport{}, false
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed {
		return tokenUsageReport{}, false
	}
	return usageTotalReport(u.turns[usageTurnKey{thread, turn}])
}

func (u *threadUsage) mentorTransition(thread, from string) {
	if u == nil {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed {
		return
	}
	if total := u.threads[thread]; total != nil {
		total.mentorFrom = from
		total.mentorRevision++
	}
}

func (u *threadUsage) mentorNote(thread, model string) (string, uint64) {
	if u == nil {
		return "", 0
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	total := u.threads[thread]
	if model == "" && total != nil {
		model = total.lastModel
	}
	if u.closed || total == nil || total.mentorFrom == "" || total.mentorFrom == model {
		return "", 0
	}
	return "Mentor " + total.mentorFrom + " → " + model, total.mentorRevision
}

func (u *threadUsage) snapshot(thread string) (tokenUsageReport, bool) {
	if u == nil {
		return tokenUsageReport{}, false
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.closed {
		report, observed := usageTotalReport(u.threads[thread])
		report.typesafe = u.typesafe[thread]
		return report, observed
	}
	return tokenUsageReport{}, false
}

func (u *threadUsage) close() {
	if u == nil {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.closed = true
	clear(u.threads)
	clear(u.typesafe)
	clear(u.turns)
}

func (t *mekugiResponseTransform) threadUsageCounts() (tokenUsageReport, bool) {
	if t.usageTracker == nil {
		return tokenUsageReport{}, false
	}
	return t.usageTracker.totals.snapshot(t.usageTracker.thread)
}

// completionUsageReport consolidates only proven descendants. Usage itself stays
// keyed by transport thread, independent of presentation names and routing sessions.
func (t *mekugiResponseTransform) completionUsageReport() (tokenUsageReport, bool) {
	if t.subagentTurn || t.usageTracker == nil {
		return tokenUsageReport{}, false
	}
	report, observed := t.threadUsageCounts()
	if !observed {
		report.Incomplete = true
	}
	rows := []agentTokenUsage{{agent: "/root", role: "main", report: report}}
	report.rows = &rows
	report.model = ""

	turn, ok := t.usageTracker.totals.turnSnapshot(t.usageTracker.thread, t.usageTracker.turnID)
	if !ok {
		turn.Incomplete = true
	}
	report.turn = &turn
	var revision uint64
	report.mentor, revision = t.usageTracker.totals.mentorNote(t.usageTracker.thread, t.usageTracker.model)
	t.usageMentorRevisions = make(map[string]uint64)
	if revision != 0 {
		t.usageMentorRevisions[t.usageTracker.thread] = revision
	}

	if t.proxy != nil && t.journalAvailable {
		journals := t.proxy.journals
		release, err := journals.lockState(t.ctx)
		if err != nil {
			report.Incomplete = true
			report.cost.known = false
			return report, true
		}
		var children []threadJournal
		read := func() error {
			var err error
			children, err = journals.descendants(t.proxy.replayStore, t.directory, t.usageTracker.thread)
			return err
		}
		if t.proxy.replayStore != nil {
			err = t.proxy.replayStore.locked(t.ctx, read)
		} else {
			err = read()
		}
		release()
		if err != nil {
			report.Incomplete = true
			report.cost.known = false
			return report, true
		}
		for _, child := range children {
			if note, revision := t.usageTracker.totals.mentorNote(child.Thread, ""); revision != 0 {
				if report.mentor != "" {
					report.mentor += " · "
				}
				report.mentor += child.Author + " " + note
				t.usageMentorRevisions[child.Thread] = revision
			}
			counts, ok := t.usageTracker.totals.snapshot(child.Thread)
			if !ok {
				counts.Incomplete = true
			}
			role := child.SpawnRole
			if role == "" {
				role = "n/a"
			}
			rows = append(rows, agentTokenUsage{agent: child.Author, role: role, report: counts})
			report.typesafe.add(counts.typesafe)
			if ^uint64(0)-report.missingUsage < counts.missingUsage {
				report.Incomplete = true
			} else {
				report.missingUsage += counts.missingUsage
			}
			report.cost.add(counts.cost)
			if !ok || report.Incomplete || !addTokenUsageCounts(&report.tokenCounts, counts.tokenCounts) {
				report.Incomplete = true
				report.cost.known = false
			}
		}
	}
	return report, true
}

func addTokenUsageCounts(sum *tokenCounts, next tokenCounts) bool {
	for _, pair := range []struct {
		dst *uint64
		add uint64
	}{
		{&sum.InputTokens, next.InputTokens},
		{&sum.UncachedInputTokens, next.UncachedInputTokens},
		{&sum.CacheWriteTokens, next.CacheWriteTokens},
		{&sum.OutputTokens, next.OutputTokens},
		{&sum.ReasoningTokens, next.ReasoningTokens},
	} {
		if ^uint64(0)-*pair.dst < pair.add {
			return false
		}
		*pair.dst += pair.add
	}
	sum.Inconsistent = sum.Inconsistent || next.Inconsistent
	return true
}

// Shared by the token report and live roster; pricing always uses the bare model.
func usageModelLabel(model, reasoning, tier string) string {
	label := strings.TrimSpace(model + " " + reasoning)
	if label != "" && (tier == "fast" || tier == "priority") {
		label += " [fast]"
	}
	return label
}
