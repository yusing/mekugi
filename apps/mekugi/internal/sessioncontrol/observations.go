package sessioncontrol

import (
	"errors"
	"strings"
	"sync"
)

// Generation identifies one live provider request, independently of host calls.
type Generation struct{ _ byte }

type generatingInput struct {
	workspace, thread string
	items             map[string]bool
}

// Observations coordinates invocation-local generation and control delivery.
// None of this state is restored, inherited or used to authorize execution.
// Source: apps/mekugi/internal/router/{tool_input_emission,app_server_btw,mekugi_proxy}.go@d002a0dc.
type Observations struct {
	mu          sync.RWMutex
	generations map[*Generation]generatingInput
	sideThreads map[string]bool
	feedbackMu  sync.Mutex
	feedback    map[[2]string][]string
}

func (o *Observations) SetToolInput(generation *Generation, workspace, thread, item string, active bool) {
	if o == nil || generation == nil || thread == "" || item == "" {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if active {
		if o.generations == nil {
			o.generations = make(map[*Generation]generatingInput)
		}
		input := o.generations[generation]
		if input.items == nil {
			input = generatingInput{workspace: workspace, thread: thread, items: make(map[string]bool)}
		}
		input.items[item] = true
		o.generations[generation] = input
	} else if input := o.generations[generation]; input.items != nil {
		delete(input.items, item)
		if len(input.items) == 0 {
			delete(o.generations, generation)
		}
	}
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
		if input.thread == thread && (input.workspace == "" || input.workspace == workspace) {
			return true
		}
	}
	return false
}

func (o *Observations) SetSideThread(thread string, active bool) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if !active {
		delete(o.sideThreads, thread)
		return
	}
	if o.sideThreads == nil {
		o.sideThreads = make(map[string]bool)
	}
	o.sideThreads[thread] = true
}

func (o *Observations) IsSideThread(thread string) bool {
	if o == nil {
		return false
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.sideThreads[thread]
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
