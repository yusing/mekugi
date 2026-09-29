package router

import (
	"context"
	"slices"
	"time"

	"github.com/yusing/mekugi/capturer"
)

func (j *threadJournal) journalCounters() *capturer.JournalMetrics {
	if j.Counters == nil {
		j.Counters = &capturer.JournalMetrics{StartedAt: time.Now().UTC().Format(time.RFC3339Nano), Operations: make(map[string]uint64)}
	}
	if j.Counters.Operations == nil {
		j.Counters.Operations = make(map[string]uint64)
	}
	return j.Counters
}

func (j *threadJournal) countJournalOperation(op string) {
	switch op {
	case "plan", "add", "set", "log", "remove", "read", "edit", "delete", "list":
		counts := j.journalCounters()
		counts.Operations[op]++
		counts.Sequence++
	}
}

// Counter receipts only deduplicate a replayed delivery of a recent response,
// so they are bounded instead of joining the journal's permanent call receipts.
const maxJournalCounterReceipts = 64

// Counter writes never append work events or move evidence/delivery cursors.
// Failure is advisory and cannot replace an accepted tool result or answer.
func (p *mekugiProxy) journalCounters(ctx context.Context, workspace, thread, receipt string, update func(*threadJournal)) {
	var snapshot *capturer.JournalMetrics
	var err error
	if update == nil && p.replayStore != nil {
		// Export-only observation reads under the store lock; a journal
		// transaction would rescan retained dependencies on every request.
		store := p.replayStore.scoped(ctx)
		err = store.locked(ctx, func() error {
			j, _, err := readThreadJournal(store, workspace, thread)
			snapshot = j.Counters.Clone()
			return err
		})
	} else {
		err = p.journals.transaction(ctx, p.replayStore, workspace, thread, func(j *threadJournal, exists bool) error {
			if !exists {
				return errJournalUnchanged
			}
			snapshot = j.Counters.Clone()
			if update == nil || receipt != "" && slices.Contains(j.CounterReceipts, receipt) {
				return errJournalUnchanged
			}
			update(j)
			if receipt != "" {
				j.CounterReceipts = append(j.CounterReceipts, receipt)
				j.CounterReceipts = j.CounterReceipts[max(0, len(j.CounterReceipts)-maxJournalCounterReceipts):]
			}
			snapshot = j.Counters.Clone()
			return nil
		})
	}
	if err != nil {
		p.notice("", thread, "journal_counters", "Journal measurement unavailable.")
		return
	}
	capturer.ObserveJournal(ctx, snapshot)
}

func (p *mekugiProxy) countJournalRead(ctx context.Context, workspace, thread, receipt, op string) {
	p.journalCounters(ctx, workspace, thread, receipt, func(j *threadJournal) { j.countJournalOperation(op) })
}
