package presentation

import (
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/libraries/pathdisplay"
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
	UserShell   bool
	GroupHeader string
	GroupStart  bool
	GroupCount  int
	// Subsumed marks original work represented by a later operation, such as staging before its commit.
	Subsumed bool
	// Effect rows belong beneath the preceding action in the same invocation.
	Effect bool

	// Output identifies the invocation aggregate (1), or a tracked segment (2+index).
	// Zero has no retained output. The attached owner resolves it within Source.
	Output int

	feed.Content
	ReplySummary, ReplyDetails                    string // Derived from the completed original body; never replaces Body.
	DetailCode                                    string `json:",omitempty"` // Original tool evidence, displayed only in details.
	Repeated                                      string
	AutoReload                                    bool   // Native compaction reader, distinct from a submitted attachment.
	AttachmentID                                  string // Durable submitted snapshot and source, shared by its chunks.
	Ended, Started                                time.Time
	Duration                                      time.Duration
	NotificationTiming                            bool
	Questions                                     []Question
	Source                                        uint64
	Operation                                     int
	Section                                       int
	Path, SyntaxPath, Workdir                     string
	ShowWorkdir                                   bool
	From, To, Owner                               string
	WaitTargets                                   []WaitTarget
	WaitTimeout, WaitReason                       string
	Results                                       *int
	Exited                                        bool
	ExitCode                                      int
	Approval                                      string
	BatchExit                                     bool
	EditOutcome                                   string
	Running, Requested, Segment, Skipped, Pending bool
	Tail                                          []string
	TailOmitted                                   int
	TailClipped, SourceOmitted                    bool // Summary bytes or source content omitted from the inline value.
	Changes                                       []ChangeRow
	Hook                                          *HookDetails
	Members                                       []Value
	RequestedOperations                           []Value
	DetailLines                                   int
	Live                                          bool
	Thought                                       time.Duration
}

type Question struct {
	Text, Answer, Options, State string
	Secret                       bool
}

// Endpoints resolves a runtime-local Main without retargeting inherited traffic.
// Other addresses retain their supplied identity, including unknown names.
func (b Value) Endpoints(local string) (from, to string) {
	endpoint := func(name string) string {
		if name == "Main" && local != "" {
			name = local
		}
		return pathdisplay.AgentCanonicalName(name)
	}
	return endpoint(b.From), endpoint(b.To)
}

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

// Disclosure names retained content omitted by the default inline value.
// Owner limits it to the agent's own transcript; other transcripts show that
// agent's complete diagnostic as incoming prose. Renderer clipping is separate.
type Disclosure struct {
	Owner string `json:"owner,omitzero"`
}

// Source: apps/mekugi/internal/webui/app/src/lib/kinds.ts:58@5bbc778a8 hasDetails.
func (b Value) Disclosure() *Disclosure {
	switch b.Kind {
	case "text", "user", "final", "message", "start", "summary", "progress":
		return nil // Authored prose and progress own their inline disclosures.
	case "error":
		if strings.TrimSpace(b.Body) != "" {
			return &Disclosure{Owner: b.Owner}
		}
		return nil
	}
	if b.TailClipped || b.SourceOmitted || b.TailOmitted > 0 || len(b.Changes) > 0 || b.EditOperation() ||
		strings.TrimSpace(b.DetailCode) != "" && strings.TrimSpace(b.DetailCode) != strings.TrimSpace(b.Code) {
		return &Disclosure{}
	}
	if b.Hook != nil {
		failed := b.Exited && b.ExitCode != 0 || b.Hook.HasError || b.Hook.Status == "failed"
		if !failed && slices.ContainsFunc(b.Tail, func(line string) bool { return strings.TrimSpace(line) != "" }) {
			return &Disclosure{}
		}
		return nil
	}
	if len(b.Questions) == 0 && strings.TrimSpace(b.Body) != "" ||
		strings.TrimSpace(b.Code) != "" && b.Label != "" && !b.Fenced && !strings.Contains(b.Code, "\n") {
		return &Disclosure{}
	}
	return nil
}

// EditOperation identifies edit rows independently of their optional writer
// label. Stock exec captures can have no recognized writer; their effect,
// grouping and retained-change navigation still belong to the invocation.
func (b Value) EditOperation() bool {
	return b.Kind == "op" && (b.EditSource != "" || slices.Contains([]string{"Create", "Edit", "Delete", "Move"}, b.Verb))
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
	case "Read", "Search", "Web Search", "Inspect", "List":
		return true
	case "Skill":
		return b.Kind == "reads" // skills-mgr run runs a program.
	}
	return false
}
