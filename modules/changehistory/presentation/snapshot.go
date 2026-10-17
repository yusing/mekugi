package presentation

import (
	"cmp"
	"errors"
	"fmt"
	"iter"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yusing/mekugi/contracts/activitydetail"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/pathdisplay"
	"github.com/yusing/mekugi/modules/changehistory/changes"
)

type AttemptKey struct{ Workspace, Call string }

type AttemptIdentity struct {
	Key                 AttemptKey
	Change, Correlation string
	Stream              int
}

// Receipt is an immutable captured-effect projection. Calls retain their order:
// the first host item anchors the receipt; siblings do not repeat its summary.
type Receipt struct {
	Thread string
	Calls  []string
	Text   string
	Pages  []activitydetail.Page
}

type Attempt struct {
	AttemptIdentity
	Operation activitydetail.Operation
	Chunks    []Chunk
	Receipt   *Receipt
}

type Counts struct {
	Callers map[string]diffmodel.Counts
	Net     *diffmodel.Counts
}

// Publication is detached, immutable saved-review content. Terminal selection
// and caller filtering cannot alter its unfiltered composition or totals.
type Publication struct {
	Files       []File
	Unfiltered  map[string]File
	Counts      Counts
	initialized bool
}

// With derives one replacement from supplied captures, preserving stable order
// and recent-update marks. Consumers share the resulting unfiltered composition.
func (p Publication) With(files []File) Publication {
	v := View{Files: p.Files, Visible: p.Unfiltered, Initialized: p.initialized}
	v.Merge(files)
	v.RefreshVisible()
	return Publication{Files: v.Files, Unfiltered: v.Visible,
		Counts: Counts{Callers: CallerCounts(v.Files), Net: NetCounts(&v)}, initialized: true}
}

// Snapshot owns exact attempts and their one derived publication. Its contents
// remain immutable after Builder.Finish, including shared receipts and slices.
type Snapshot struct {
	Publication Publication
	attempts    map[AttemptKey]Attempt
	order       []AttemptKey
	receipts    []*Receipt // Immutable references, assembled only during hydration.
	bytes       int
}

func (s *Snapshot) Attempts() iter.Seq[Attempt] {
	return func(yield func(Attempt) bool) {
		if s != nil {
			for _, key := range s.order {
				if !yield(s.attempts[key]) {
					return
				}
			}
		}
	}
}

func (s *Snapshot) Len() int {
	if s == nil {
		return 0
	}
	return len(s.order)
}

func (s *Snapshot) Receipts() []*Receipt {
	if s == nil {
		return nil
	}
	return s.receipts
}

// Builder belongs to one unpublished hydration job. Previously accepted data
// is shared read-only; new attempts cannot escape a failed or stale job.
type Builder struct {
	base     *Snapshot
	next     Snapshot
	finished bool
}

func NewBuilder(base *Snapshot) *Builder {
	b := &Builder{base: base}
	if base != nil {
		b.next = *base
	}
	b.next.attempts = maps.Clone(b.next.attempts)
	if b.next.attempts == nil {
		b.next.attempts = make(map[AttemptKey]Attempt)
	}
	b.next.order = slices.Clone(b.next.order)
	b.next.receipts = slices.Clone(b.next.receipts)
	return b
}

// Known validates repeated identities before the application reopens evidence.
func (b *Builder) Known(identity AttemptIdentity) (bool, error) {
	if old, found := b.next.attempts[identity.Key]; found {
		if old.AttemptIdentity != identity {
			return false, errors.New("live diff attempt identity changed")
		}
		return true, nil
	}
	if len(b.next.order) >= 65536 {
		return false, errors.New("live diff exceeds 65,536 attempts")
	}
	return false, nil
}

func (b *Builder) Add(attempt Attempt) error {
	if b.finished {
		return errors.New("saved-review builder is already published")
	}
	if found, err := b.Known(attempt.AttemptIdentity); found || err != nil {
		return err
	}
	if !filepath.IsAbs(attempt.Key.Workspace) {
		return errors.New("live diff workspace must be absolute")
	}
	bytes := b.next.bytes
	if attempt.Receipt != nil {
		receipt := *attempt.Receipt
		receipt.Calls, receipt.Pages = slices.Clone(receipt.Calls), slices.Clone(receipt.Pages)
		attempt.Receipt = &receipt
		bytes += len(receipt.Text)
	}
	attempt.Chunks = slices.Clone(attempt.Chunks)
	for _, chunk := range attempt.Chunks {
		bytes += len(chunk.Review.Diff) + len(chunk.Review.BeforePath) + len(chunk.Review.AfterPath) + len(attempt.Key.Workspace) + 1 + len(attempt.Key.Call) + len(chunk.Caller) + len(chunk.Source)
	}
	if bytes > changes.MaxReadBytes {
		return errors.New("live diff exceeds 64 MiB; use mchanges with a narrower range")
	}
	b.next.bytes = bytes
	b.next.attempts[attempt.Key] = attempt
	b.next.order = append(b.next.order, attempt.Key)
	if attempt.Receipt != nil {
		b.next.receipts = append(b.next.receipts, attempt.Receipt)
	}
	return nil
}

func (b *Builder) Finish() *Snapshot {
	if b.finished {
		return &b.next
	}
	b.finished = true
	if b.base != nil && b.base.Publication.initialized && len(b.next.order) == len(b.base.order) {
		return b.base
	}
	var captures []Chunk
	for _, key := range b.next.order {
		captures = append(captures, b.next.attempts[key].Chunks...)
	}
	b.next.Publication = b.next.Publication.With(GroupCaptures(captures))
	return &b.next
}

func (b *Builder) RequirePresent(present map[AttemptKey]bool) error {
	for _, key := range b.next.order {
		if !present[key] {
			return errors.New("change records were removed; restart the live view")
		}
	}
	return nil
}

// ActivityDetail resolves the original captured invocation, never a composed
// successor or current workspace contents. Source: apps/mekugi/internal/router/native_edit_navigation.go:18:74@[5927a739].
func (s *Snapshot) ActivityDetail(request activitydetail.Request) activitydetail.Result {
	if request.Thread == "" || request.Item == "" {
		return activitydetail.Result{}
	}
	for attempt := range s.Attempts() {
		match := attempt.Operation == request.Operation
		if receipt := attempt.Receipt; receipt != nil {
			match = receipt.Thread == request.Thread && slices.Contains(receipt.Calls, request.Item)
		}
		if !match || len(attempt.Chunks) == 0 {
			continue
		}
		var result activitydetail.Result
		for i, chunk := range attempt.Chunks {
			workspace := cmp.Or(chunk.Workspace, attempt.Key.Workspace)
			target := pathdisplay.ForWorkspace(workspace, cmp.Or(chunk.Review.AfterPath, chunk.Review.BeforePath))
			page := activitydetail.Page{ID: "changehistory:" + cmp.Or(chunk.Key, fmt.Sprintf("%s\x00%s/%d", attempt.Key.Workspace, attempt.Key.Call, i)),
				Verb: chunk.Review.Action().Title(), Path: target, Language: "diff",
				Code: chunk.Review.UnifiedDiffWithPaths(pathdisplay.ForWorkspace(workspace, chunk.Review.BeforePath), pathdisplay.ForWorkspace(workspace, chunk.Review.AfterPath))}
			result.Pages = append(result.Pages, page)
			if result.Selected == "" && (request.Path == "" || request.Path == target ||
				chunk.Review.Action().Title() == "Move" && request.Path == pathdisplay.Move(workspace, chunk.Review.BeforePath, chunk.Review.AfterPath) ||
				request.Directory && strings.HasPrefix(target, strings.TrimSuffix(request.Path, "/")+"/")) {
				result.Selected = page.ID
			}
		}
		if result.Selected != "" {
			return result
		}
	}
	return activitydetail.Result{}
}
