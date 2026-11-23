package sessioncontrol

import (
	"crypto/rand"
	"errors"
	"strconv"
	"strings"
	"sync"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
)

// Generation identifies one live provider request, independently of host calls.
type Generation struct{ _ byte }

// GenerationItem joins the identities available on admission and completion.
// Some host-supported streams omit the output index or assign an ID only later.
type GenerationItem struct {
	ID      string
	Index   int
	Indexed bool
}

type generatingInput struct {
	workspace, thread string
	items             map[GenerationItem]generatingItem
}

type generatingItem struct {
	identity *activitypresentation.ToolGeneration
	order    uint64
}

// Observations coordinates invocation-local generation and control delivery.
// None of this state is restored, inherited or used to authorize execution.
// Source: apps/mekugi/internal/router/{tool_input_emission,app_server_btw,mekugi_proxy}.go@d002a0dc.
type Observations struct {
	mu                sync.RWMutex
	generations       map[*Generation]generatingInput
	nextTool          uint64
	feedbackMu        sync.Mutex
	feedback          map[[2]string][]string
	contextIdentity   string
	contextRevision   map[string]uint64
	contextCompaction map[string]string
}

// ObserveCompactionContext consumes the request's native context-window identity,
// or a historical summary reference when that identity is unavailable. Refresh
// precedes inference independently of queued presentation events.
func (o *Observations) ObserveCompactionContext(thread, reference string) {
	if o == nil || thread == "" || reference == "" {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.contextCompaction == nil {
		o.contextCompaction = make(map[string]string)
	}
	if o.contextCompaction[thread] == reference {
		return
	}
	o.contextCompaction[thread] = reference
	if o.contextRevision == nil {
		o.contextRevision = make(map[string]uint64)
	}
	o.contextRevision[thread]++
}

// RefreshContext records an authoritative fresh/resumed/replaced context or
// completed compaction. Acquisition stays lazy until the next generating turn.
func (o *Observations) RefreshContext(thread string) {
	if o == nil || thread == "" {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.contextRevision == nil {
		o.contextRevision = make(map[string]uint64)
	}
	o.contextRevision[thread]++
}

// ContextIdentity distinguishes live history across reset, rewind and cold resume.
// The invocation identity prevents restored cursors matching a reused revision.
func (o *Observations) ContextIdentity(thread string) string {
	if o == nil || thread == "" {
		return ""
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.contextIdentity == "" {
		o.contextIdentity = rand.Text()
	}
	return o.contextIdentity + ":" + thread + ":" + strconv.FormatUint(o.contextRevision[thread], 10)
}

// ContextMatches checks the incoming native window before instruction projection
// observes it. This does not advance or replace the existing context revision.
func (o *Observations) ContextMatches(thread, identity, reference string) bool {
	if o == nil || identity == "" {
		return false
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	return identity == o.contextIdentity+":"+thread+":"+strconv.FormatUint(o.contextRevision[thread], 10) &&
		(reference == "" || reference == o.contextCompaction[thread])
}

func (o *Observations) SetToolInput(generation *Generation, workspace, thread string, item GenerationItem, active bool) {
	if o == nil || generation == nil || thread == "" || item.ID == "" && !item.Indexed {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	input := o.generations[generation]
	item = input.identifyItem(item)
	if active {
		if o.generations == nil {
			o.generations = make(map[*Generation]generatingInput)
		}
		if input.items == nil {
			input = generatingInput{workspace: workspace, thread: thread, items: make(map[GenerationItem]generatingItem)}
		}
		if _, exists := input.items[item]; !exists {
			input.items[item] = generatingItem{}
		}
		o.generations[generation] = input
	} else if input.items != nil {
		delete(input.items, item)
		if len(input.items) == 0 {
			delete(o.generations, generation)
		}
	}
}

// IdentifyGenerationItem retains identities learned between admission and
// delivery. It never admits a new item or releases an unfinished boundary.
func (o *Observations) IdentifyGenerationItem(generation *Generation, item GenerationItem) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.generations[generation].identifyItem(item)
}

// IdentifyToolInput enriches the existing request-scoped input observation.
// Unknown tools keep their supplied identity rather than disappearing.
func (o *Observations) IdentifyToolInput(generation *Generation, item GenerationItem, name string, javascript bool, delta string) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	input := o.generations[generation]
	key := input.identifyItem(item)
	value, exists := input.items[key]
	if !exists {
		return
	}
	if value.identity == nil {
		if javascript {
			name = ""
		}
		value.identity = activitypresentation.NewToolGeneration(name, javascript)
		o.nextTool++
		value.order = o.nextTool
	}
	value.identity.Append(delta)
	input.items[key] = value
}

// ToolInput returns the newest still-generating tool for the admitted thread.
func (o *Observations) ToolInput(thread string) string {
	if o == nil {
		return ""
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	var label string
	var order uint64
	for _, input := range o.generations {
		if input.thread == thread {
			for _, value := range input.items {
				if value.identity != nil && value.order > order {
					label, order = value.identity.Label(), value.order
				}
			}
		}
	}
	return label
}

func (input generatingInput) identifyItem(item GenerationItem) GenerationItem {
	for admitted, value := range input.items {
		// Indices disambiguate items when both events carry them. Otherwise,
		// a stable ID joins an indexed event to an ID-only counterpart.
		matches := admitted.ID != "" && admitted.ID == item.ID
		if admitted.Indexed && item.Indexed {
			matches = admitted.Index == item.Index
		}
		if matches {
			identified := admitted
			if identified.ID == "" {
				identified.ID = item.ID
			}
			if !identified.Indexed && item.Indexed {
				identified.Index, identified.Indexed = item.Index, true
			}
			if identified != admitted {
				delete(input.items, admitted)
				input.items[identified] = value
			}
			return identified
		}
	}
	return item
}

func (o *Observations) EndGeneration(generation *Generation) {
	if o != nil {
		o.mu.Lock()
		delete(o.generations, generation)
		o.mu.Unlock()
	}
}

func (o *Observations) EmittingToolInput(workspace, thread string) bool {
	if o == nil {
		return false
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	for _, input := range o.generations {
		// Missing cwd does not erase native thread identity. This observation
		// resolves no filesystem operand.
		if input.thread == thread && (input.workspace == "" || input.workspace == workspace) && len(input.items) > 0 {
			return true
		}
	}
	return false
}

func (o *Observations) AnswerWithFeedback(thread, turn, text string, answer func() error) error {
	if o == nil {
		return errors.New("native denial feedback requires session control")
	}
	o.feedbackMu.Lock()
	defer o.feedbackMu.Unlock()
	if err := answer(); err != nil {
		return err
	}
	if o.feedback == nil {
		o.feedback = make(map[[2]string][]string)
	}
	key := [2]string{thread, turn}
	o.feedback[key] = append(o.feedback[key], text)
	return nil
}

func (o *Observations) ClearFeedback(thread, turn string) {
	if o != nil {
		o.feedbackMu.Lock()
		delete(o.feedback, [2]string{thread, turn})
		o.feedbackMu.Unlock()
	}
}

func (o *Observations) Feedback(thread, turn string) string {
	if o == nil {
		return ""
	}
	o.feedbackMu.Lock()
	defer o.feedbackMu.Unlock()
	return strings.Join(o.feedback[[2]string{thread, turn}], "\n\n")
}
