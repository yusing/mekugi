package presentation

import (
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/contracts/observation"

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
	CompactionMethod observation.CompactionMethod `json:"-"`
	UserShell        bool
	GroupHeader      string
	GroupStart       bool
	GroupCount       int
	// Subsumed marks original work represented by a later operation, such as staging before its commit.
	Subsumed bool
	// Effect rows belong beneath the preceding action in the same invocation.
	Effect bool

	// Output identifies the invocation aggregate (1), or a tracked segment (2+index).
	// Zero has no retained output. The attached owner resolves it within Source.
	Output int

	feed.Content
	// ReplyDetails is the supporting section, or the headingless excerpt body
	// when ReplySummary is empty. Body always retains the original source.
	ReplySummary, ReplyDetails                    string
	DetailCode                                    string `json:",omitempty"` // Original tool evidence, displayed only in details.
	Repeated                                      string
	AutoReload                                    bool   // Native compaction reader, distinct from a submitted attachment.
	AttachmentKind                                string // file, skill, change, selection or omission.
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
	Failed                                        bool // Compact values retain the observed outcome without tool evidence.
}

// FailedOutcome shares operation failure semantics across full and compact rows.
func (b Value) FailedOutcome() bool {
	return b.Failed || b.Verb == "Attach failed" || b.ExitCode != 0 || b.Hook != nil &&
		(b.Hook.Failed() || slices.Contains([]string{"blocked", "stopped"}, b.Hook.Status))
}

// ReadSubject preserves literal queries and skill identities, shortening file paths.
func (b Value) ReadSubject() string {
	var names []string
	for _, read := range b.Reads {
		name := read.Path
		if b.Verb != "Search" && b.Verb != "Web Search" && b.Verb != "Skill" {
			name = name[strings.LastIndex(name, "/")+1:]
		}
		names = append(names, markdownsyntax.QuoteCode(name))
	}
	return strings.Join(names, ", ")
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

// Failed distinguishes hook errors from blocked or stopped execution.
func (h HookDetails) Failed() bool { return h.HasError || h.Status == "failed" }

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
	case "attachments":
		for _, member := range b.Members {
			if member.Disclosure() != nil || member.Output != 0 || len(member.Tail) > 0 || member.Code != "" {
				return &Disclosure{}
			}
		}
		return nil
	case "compaction":
		if b.CompactionMethod == observation.ContextReset {
			return nil
		}
		return &Disclosure{}
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
	return b.Kind == "attachments" || b.Verb == "Read" || b.Verb == "Skill" && b.Kind == "reads" ||
		b.Kind == "op" && (b.Verb == "Attached" || b.Verb == "Attached skill" || b.Verb == "Attached selection")
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

// Committed requires a completed operation and observed commit output.
func (b Value) Committed() bool {
	return b.Verb == "Commit" && !b.Running && !b.Skipped && b.ExitCode == 0 &&
		slices.ContainsFunc(b.Changes, func(row ChangeRow) bool { return row.Footer }) && b.VCS()
}

// Source: modules/activity/terminalview/blocks.go:126:179@f7b0377da.
func (b Value) RowVerb() string {
	if b.UserShell {
		if b.Running {
			return "You running"
		}
		return "You ran"
	}
	if b.Hook != nil {
		switch b.Hook.Status {
		case "running":
			if b.Running {
				return "Hook Running"
			}
		case "completed":
			return "Hook Ran"
		case "failed":
			return "Hook Failed"
		case "blocked":
			return "Hook Blocked"
		case "stopped":
			return "Hook Stopped"
		}
		return "Hook Run"
	}
	switch {
	case b.Pending:
		return "Pending"
	case b.Skipped && b.Verb == "Run":
		return "Skipped"
	case b.Kind == "op" && b.Verb == "Run" && b.Requested:
		return "Run"
	case b.Kind == "op" && b.Verb == "Run" && !b.Running && strings.HasPrefix(b.Approval, "Pending Approval"):
		return "Run"
	case b.Kind == "op" && b.Verb == "Run" && b.Running:
		return "Running"
	case b.Kind == "op" && b.Verb == "Run" && !b.Skipped:
		return "Ran"
	case b.Committed():
		return "Committed"
	case b.EditOperation():
		return b.EditVerb()
	}
	return b.Verb
}

// EditVerb is a grouped row's verb: past tense once confirmed, and otherwise
// the requested action, whose status the group's first row names.
func (b Value) EditVerb() string {
	if b.GroupHeader != "Edited" {
		return b.Verb
	}
	switch b.Verb {
	case "Create":
		return "Created"
	case "Delete":
		return "Deleted"
	case "Move":
		return "Moved"
	}
	return "Edited"
}
