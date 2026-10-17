// Source: apps/mekugi/internal/router/exec_track.go@[c6bad96c].
package router

import (
	"slices"

	"github.com/yusing/mekugi/apps/mekugi/internal/frontends"
	"github.com/yusing/mekugi/libraries/commandanalysis"
	"github.com/yusing/mekugi/libraries/commandanalysis/execscope"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/modules/commandsegments/segments"
)

// execPreviewTrack interprets matched reports within the application's admitted
// writer scope. The subscription itself neither captures nor publishes files.
type execPreviewTrack struct {
	*segments.Subscription
	writers    map[string]bool
	streamable map[string]bool
}

func newExecPreviewTrack(observer *segments.Observer, thread, turn string, commands []commandanalysis.Command) *execPreviewTrack {
	scripts := make([]string, len(commands))
	for i, command := range commands {
		scripts[i] = command.Command
	}
	subscription := observer.Subscribe(thread, turn, scripts)
	if subscription == nil {
		return nil
	}
	p := &execPreviewTrack{Subscription: subscription, writers: make(map[string]bool)}
	for _, command := range commands {
		plan := execscope.Classify(execscope.Input{Command: command.Command, Directory: command.Workdir, Shell: command.Shell})
		p.writers[p.Source(command.Command, "")] = plan.Class != execscope.Neutral && (len(plan.Programs) == 0 || slices.ContainsFunc(plan.Programs, func(program execscope.Program) bool { return program.Direct }))
	}
	return p
}

type execPreviewState struct {
	tracked, settled bool
	completed        int
	calls            []string
	changed          <-chan struct{}
}

func (p *execPreviewTrack) state() (state execPreviewState) {
	if p == nil {
		return state
	}
	observed := p.State()
	state.changed, state.settled = observed.Changed, true
	allTracked, writing := true, false
	for _, retained := range observed.Matches {
		script := retained.Source
		if retained.Ambiguous {
			return execPreviewState{tracked: true, changed: observed.Changed}
		}
		match := retained.Report
		streamable := p.streamable == nil || p.streamable[script]
		if match != nil && retained.HostCompleted {
			state.completed++
			for _, segment := range match.Segments {
				if segment.Ended {
					state.completed++
				}
			}
			state.calls = append(state.calls, retained.Key[2])
			state.tracked = state.tracked || streamable
			continue
		}
		if match == nil || match.HostOnly || match.Ended && !match.Done {
			allTracked = allTracked && !streamable
			writing = writing || match != nil && p.writers[script]
			continue
		}
		state.tracked = state.tracked || streamable
		last, completed := match.LastStarted(), 0
		for i, segment := range match.Segments {
			if segment.Ended {
				completed++
			}
			writer := false
			if p.streamable != nil {
				writer = p.streamable[p.Source(segment.Source, "")]
			} else {
				writer = (activitypresentation.CommandSource{Script: segment.Source, Display: frontends.ShellDisplaySource(segment.Source), Directory: ""}).WritesFiles()
			}
			if streamable && writer && !segment.Ended && !match.Done && (segment.Began || i > last) {
				state.settled = false
			}
		}
		// A receipt covers observed endpoints of this invocation. A later
		// command in the same shell does not hold its earlier edits hostage.
		// A sibling with no completed boundary cannot lend its writes to it.
		writing = writing || completed == 0 && last >= 0 && p.writers[script] && !match.Done
		if completed > 0 {
			state.completed += completed
			state.calls = append(state.calls, retained.Key[2])
		}
	}
	state.tracked = state.tracked && allTracked
	state.settled = state.tracked && state.settled
	if writing {
		state.completed, state.calls = 0, nil
	}
	return state
}
