package presentation

import (
	"time"

	"github.com/yusing/mekugi/modules/activity/feed"
)

// CommandSource is authenticated display source, not executable input. The
// backend adapter removes its own instrumentation and supplies the directory.
type CommandSource struct{ Script, Display, Directory string }

type CommandCompletion struct {
	Source    CommandSource
	At        time.Time
	Duration  time.Duration
	Aggregate *string
	Exit      *int
}

// Invocation owns one admitted host command, independently of turn lifetime and
// terminal pacing. The application's operation index and retained feed records
// reference this same object; it has no registry, process or storage capability.
type Invocation struct {
	source    CommandSource
	started   time.Time
	ended     time.Time
	duration  time.Duration
	complete  bool
	exited    bool
	exit      int
	output    *feed.Output
	retention *feed.Retention
	report    invocationReport
}

// NewInvocation accepts a zero start when only completion evidence is available.
// Nil retention preserves lifecycle and source without retaining full output.
// Source: apps/mekugi/internal/router/app_server_events.go:354:368@[26c7bc92].
func NewInvocation(source CommandSource, started time.Time, retention *feed.Retention) *Invocation {
	i := &Invocation{source: source, started: started, retention: retention}
	if retention != nil {
		i.output = retention.New()
	}
	return i
}

func (i *Invocation) Write(delta string) {
	if !i.complete && i.output != nil {
		i.output.Write(delta)
	}
}

// Complete publishes host facts before report settlement or terminal delivery.
// Source: apps/mekugi/internal/router/app_server_events.go:371:380,619:634@[26c7bc92].
func (i *Invocation) Complete(done CommandCompletion) {
	if i.complete {
		return
	}
	i.source, i.ended, i.duration, i.complete = done.Source, done.At, done.Duration, true
	if done.Exit != nil {
		i.exited, i.exit = true, *done.Exit
	}
	if i.output != nil {
		i.output.Finish(done.Aggregate, done.Exit)
	}
}

func (i *Invocation) Running() bool { return i != nil && !i.complete }

// Output supplies the existing application's retained-detail binding. Renderer
// publications use Snapshot instead of this mutable owner handle.
func (i *Invocation) Output() *feed.Output { return i.output }

func (i *Invocation) Snapshot() Command {
	c := Command{Script: i.source.Script, Display: i.source.Display, Directory: i.source.Directory,
		Started: i.started, Ended: i.ended, Duration: i.duration, Running: !i.complete, Exited: i.exited, ExitCode: i.exit}
	if i.output != nil {
		output, _ := i.output.Context(0)
		c.Output, c.OutputContent = &output, i.output.HasContent()
	}
	return c
}
