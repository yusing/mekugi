package presentation

import (
	"regexp"
	"time"

	"github.com/yusing/mekugi/libraries/sourceanalysis/markdownsyntax"
	"github.com/yusing/mekugi/modules/activity/feed"
)

var directoryDeletionPattern = regexp.MustCompile(`^ • [0-9]+ files?(?: · .*)?$`)

func (b Value) DirectoryDeletion() bool {
	_, end, ok := markdownsyntax.CodeSpan(b.Label, 0)
	return b.Verb == "Delete" && ok && directoryDeletionPattern.MatchString(b.Label[end:])
}

// Value is an enriched activity publication. Source and Operation identify the
// observed item and its operation. Members is a flat list of original values,
// shared by feed grouping, navigation and detail pages, never another transcript.
// Values contain source text and bounded output facts, not renderer state or
// handles into the attached output owner.
type Value struct {
	GroupHeader string
	GroupStart  bool
	GroupCount  int
	// Subsumed marks original work represented by a later operation, such as staging before its commit.
	Subsumed bool

	// Output identifies the invocation aggregate (1), or a tracked segment (2+index).
	// Zero has no retained output. The attached owner resolves it within Source.
	Output int

	feed.Content
	Repeated                             string
	Ended, Started                       time.Time
	Duration                             time.Duration
	NotificationTiming                   bool
	Questions                            []Question
	Source                               uint64
	Operation                            int
	Section                              int
	Path, SyntaxPath, Workdir            string
	ShowWorkdir                          bool
	From, To, Owner                      string
	WaitTargets                          []WaitTarget
	Results                              *int
	Exited                               bool
	ExitCode                             int
	Approval                             string
	BatchExit                            bool
	EditOutcome                          string
	Running, Requested, Segment, Skipped bool
	Tail                                 []string
	TailOmitted                          int
	Changes                              []ChangeRow
	Hook                                 *HookDetails
	Members                              []Value
	RequestedOperations                  []Value
	DetailLines                          int
	Live                                 bool
	Thought                              time.Duration
}

type Question struct{ Text, Answer, Options, State string }
type WaitTarget struct{ Name, Status string }
type HookDetails struct {
	HandlerType, ExecutionMode, Status string
	HasError                           bool
}
type ChangeRow struct {
	OmittedLines            int // Source rows omitted before the retained change summary.
	Verb, Code, From, Label string
	Added, Removed          int
	Note                    string
	Footer                  bool
}

// EditPath returns the unshortened target of an edit row, when known.
// Source: modules/activity/terminalview/blocks.go:225:229@6d403a5a.
func (b Value) EditPath() string {
	path, _, _ := markdownsyntax.CodeSpan(b.Label, 0)
	return path
}

// ReadOutput reports a file or skill read, whose output is what the agent
// read rather than a result to watch, so it starts collapsed.
func (b Value) ReadOutput() bool {
	return b.Verb == "Read" || b.Verb == "Skill" && b.Kind == "reads" ||
		b.Kind == "op" && (b.Verb == "Attached" || b.Verb == "Attached skill")
}

// MarkdownOutput reports skill content, rather than a skill program's output.
func (b Value) MarkdownOutput() bool {
	return (b.Verb == "Skill" || b.Verb == "Attached skill") && b.ReadOutput()
}

// Instant reports an operation that prints what it reads at once: a read,
// search, listing or inspection, not a program whose output is worth
// watching roll by.
func (b Value) Instant() bool {
	switch b.Verb {
	case "Read", "Search", "Inspect", "List":
		return true
	case "Skill":
		return b.Kind == "reads" // skills-mgr run runs a program.
	}
	return false
}
