package sessionapi

import (
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/libraries/pathdisplay"
	"github.com/yusing/mekugi/libraries/textsanitize"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"
	"github.com/yusing/mekugi/modules/livepreview/preview"
)

// Frame is one detached renderer input. Launch builds it from admitted owners;
// the server derives snapshots and patches from consecutive frames.
type Frame struct {
	Session    Session
	Playback   *Playback // Absent for a live attachment.
	Input      *Input    // Absent for a replay.
	Prompts    *Prompts  // Absent for a replay.
	Attachment uint64    // A change restarts entry sequences.
	Status     sessionpresentation.Label
	Caption    sessionpresentation.Caption // Runtime facts for admitted descendant viewers.
	Main       *activitypresentation.Publication
	// Agents is the child feed: each agent's own activity, and the roster.
	Agents *activitypresentation.Publication
	Review *changepresentation.ReviewView
	Live   []preview.Preview
	// ChildReplySummary is the invocation's child_reply_summary feature.
	ChildReplySummary bool
}

type Session struct {
	Thread    string `json:"thread"`
	Title     string `json:"title,omitzero"`
	Workspace string `json:"workspace,omitzero"`
	Started   int64  `json:"started,omitzero"`
}

// Input is what a live session's input can do now.
type Input struct {
	Reason    string `json:"reason,omitzero"` // Why Main cannot take input now.
	Stoppable bool   `json:"stoppable,omitzero"`
	Waiting   int    `json:"waiting,omitzero"` // Questions and approvals Main waits on.
}

type Prompts struct {
	Questions []QuestionCall `json:"questions"`
	Approvals []Approval     `json:"approvals"`
}

type QuestionCall struct {
	ID        string     `json:"id"`
	Agent     string     `json:"agent,omitzero"`
	Questions []Question `json:"questions"`
}

type Question struct {
	ID      string   `json:"id"`
	Header  string   `json:"header,omitzero"`
	Text    string   `json:"text,omitzero"`
	Options []Option `json:"options,omitempty"`
	Secret  bool     `json:"secret,omitzero"`
}

type Option struct {
	Label       string `json:"label"`
	Description string `json:"description,omitzero"`
}

type Approval struct {
	ID      string   `json:"id"`
	Agent   string   `json:"agent,omitzero"`
	Title   string   `json:"title"`
	Subject string   `json:"subject,omitzero"`
	Details []string `json:"details"`
	Choices []Choice `json:"choices"`
}

type Choice struct {
	ID    int    `json:"id"`
	Label string `json:"label"`
	Deny  bool   `json:"deny,omitzero"`
}

// Summary is a session's entry in the session list.
type Summary struct {
	Replay    bool
	Thread    string
	Title     string
	Workspace string
	Started   time.Time
	Status    sessionpresentation.Label
	Ready     bool // Main can accept the initial hosted message. Private to the hub.
	Waiting   int  // Questions and approvals Main waits on.
}

// listed is a detached thread-list entry addressed by logical identity.
type listed struct {
	ParentThread string `json:"parentThread,omitzero"`
	ID           string `json:"id,omitzero"`
	Starting     bool   `json:"starting,omitzero"`
	Replay       bool   `json:"replay,omitzero"`
	Thread       string `json:"thread,omitzero"`
	Title        string `json:"title,omitzero"`
	Workspace    string `json:"workspace,omitzero"`
	Started      int64  `json:"started,omitzero"`
	Status       status `json:"status"`
	Waiting      int    `json:"waiting,omitzero"`
}

func projectSummary(s Summary) listed {
	return listed{
		Replay: s.Replay, Thread: s.Thread, Title: textsanitize.Safe(s.Title, false),
		Workspace: s.Workspace, Started: millis(s.Started), Status: projectStatus(s.Status), Waiting: s.Waiting,
	}
}

type Playback struct {
	Position  int64   `json:"position"` // Recorded milliseconds.
	Until     int64   `json:"until"`
	Speed     float64 `json:"speed"`
	Paused    bool    `json:"paused,omitzero"`
	Simulated bool    `json:"simulated,omitzero"`
}

type status struct {
	Text      string `json:"text,omitzero"`
	Failure   string `json:"failure,omitzero"`
	Alert     bool   `json:"alert,omitzero"`
	Running   bool   `json:"running,omitzero"`
	Completed *int64 `json:"completed,omitzero"`
}

func projectStatus(label sessionpresentation.Label) status {
	s := status{Text: label.Text, Alert: label.Alert, Running: label.Running}
	if label.FailureKnown {
		s.Failure = label.Failure
	}
	if label.CompletionKnown {
		s.Completed = new(label.Completion.Milliseconds())
	}
	return s
}

type entry struct {
	Spans          []activitypresentation.TextSpan `json:"spans,omitzero"`
	Images         []string                        `json:"images,omitzero"`
	Seq            uint64                          `json:"seq"`
	Agent          string                          `json:"agent,omitzero"`
	Kind           string                          `json:"kind,omitzero"`
	Text           string                          `json:"text,omitzero"`
	Observed       int64                           `json:"observed,omitzero"`
	Running        bool                            `json:"running,omitzero"`
	Assignment     bool                            `json:"assignment,omitzero"`
	AssignmentTo   string                          `json:"assignmentTo,omitzero"`
	AssignmentText string                          `json:"assignmentText,omitzero"`
	Values         []value                         `json:"values,omitzero"`
}

type value struct {
	Kind        string     `json:"kind,omitzero"`
	Verb        string     `json:"verb,omitzero"`
	BatchExit   bool       `json:"batchExit,omitzero"` // A compound command's combined result.
	Label       string     `json:"label,omitzero"`
	Sources     []string   `json:"sources,omitzero"` // Programs that made a file edit, outside its label.
	Path        string     `json:"path,omitzero"`
	AutoReload  bool       `json:"autoReload,omitzero"`
	Approval    string     `json:"approval,omitzero"`
	Code        string     `json:"code,omitzero"`
	DetailCode  string     `json:"detailCode,omitzero"`
	Lang        string     `json:"lang,omitzero"`
	Fenced      bool       `json:"fenced,omitzero"`
	Body        string     `json:"body,omitzero"`
	Summary     string     `json:"summary,omitzero"` // A child reply's own summary of Body.
	Reads       []read     `json:"reads,omitzero"`
	Subsumed    bool       `json:"subsumed,omitzero"`
	Effect      bool       `json:"effect,omitzero"`
	Pending     bool       `json:"pending,omitzero"`
	HiddenEdits int        `json:"hiddenEdits,omitzero"` // Count of retained rows folded from this index.
	FoldSource  bool       `json:"foldSource,omitzero"`
	Duration    int64      `json:"duration,omitzero"`
	Thought     int64      `json:"thought,omitzero"`
	Workdir     string     `json:"workdir,omitzero"`
	From        string     `json:"from,omitzero"` // Caller paths of a message's ends.
	To          string     `json:"to,omitzero"`
	Questions   []question `json:"questions,omitzero"`
	Waits       []wait     `json:"waits,omitzero"`
	WaitReason  string     `json:"waitReason,omitzero"`
	WaitTimeout string     `json:"waitTimeout,omitzero"`
	ExitCode    *int       `json:"exitCode,omitzero"`
	EditOutcome string     `json:"editOutcome,omitzero"`
	Running     bool       `json:"running,omitzero"`
	Live        bool       `json:"live,omitzero"`
	Tail        []string   `json:"tail,omitzero"`
	TailOmitted int        `json:"tailOmitted,omitzero"`
	Changes     []change   `json:"changes,omitzero"`
	Hook        *hook      `json:"hook,omitzero"`
}

type read struct {
	Path   string   `json:"path"`
	Ranges []string `json:"ranges,omitzero"`
}

type question struct {
	Text   string `json:"text,omitzero"`
	Answer string `json:"answer,omitzero"`
}

type wait struct {
	Name   string `json:"name"`
	Status string `json:"status,omitzero"`
}

type change struct {
	Verb    string `json:"verb,omitzero"`
	Code    string `json:"code,omitzero"`
	Label   string `json:"label,omitzero"`
	Added   int    `json:"added,omitzero"`
	Removed int    `json:"removed,omitzero"`
	Note    string `json:"note,omitzero"`
	Footer  bool   `json:"footer,omitzero"`
}

type hook struct {
	Status   string `json:"status,omitzero"`
	HasError bool   `json:"hasError,omitzero"`
}

type roster struct {
	Agents  []agent `json:"agents"`
	Added   int     `json:"added,omitzero"` // Net workspace lines, when known.
	Removed int     `json:"removed,omitzero"`
}

type agent struct {
	Name         string   `json:"name"`            // Caller path, the agent's identity.
	Thread       string   `json:"thread,omitzero"` // Runtime identity; admission remains server-owned.
	Label        string   `json:"label"`           // Display name.
	Role         string   `json:"role,omitzero"`
	Depth        int      `json:"depth,omitzero"`
	State        string   `json:"state"`
	Turns        uint64   `json:"turns,omitzero"`
	Roundtrips   uint64   `json:"roundtrips,omitzero"`
	Work         *int64   `json:"work,omitzero"`
	WorkSince    int64    `json:"workSince,omitzero"`
	LastResponse int64    `json:"lastResponse,omitzero"`
	Cost         *float64 `json:"cost,omitzero"`
	CostPartial  bool     `json:"costPartial,omitzero"`
	InputTokens  *uint64  `json:"inputTokens,omitzero"`
	OutputTokens *uint64  `json:"outputTokens,omitzero"`
	UsagePartial bool     `json:"usagePartial,omitzero"`
	Context      *uint64  `json:"context,omitzero"`
	Window       uint64   `json:"window,omitzero"`
	Added        *int     `json:"added,omitzero"`
	Removed      *int     `json:"removed,omitzero"`
	Skills       []string `json:"skills,omitzero"`
	Items        []string `json:"items,omitzero"`
}

func millis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// projectEntry carries child reply summaries only when summaries is set.
func projectEntry(e activitypresentation.Entry, summaries bool) entry {
	e = publicEntry(e)
	p := entry{
		Seq: e.Seq, Agent: e.Agent, Kind: e.Kind, Text: e.Text, Observed: millis(e.Observed), Running: e.Running,
		Assignment: e.Assignment, AssignmentTo: e.AssignmentTo, AssignmentText: e.AssignmentText,
		Values: projectValues(e.Values, summaries), Spans: e.Spans,
	}
	for _, source := range e.Images {
		p.Images = append(p.Images, imageIdentity(source))
	}
	// A compound invocation ends in one result row for its exit and output,
	// which the terminal labels generically. Under the classified steps the
	// browser shows only the result, since the steps already show what ran; a
	// result row with no steps shows the command that ran.
	command := ""
	if c := e.Command; c != nil {
		command = c.Command.Display
		if command == "" && c.Details != nil {
			command = c.Details.CommandText
		}
		command = textsanitize.Safe(command, false)
	}
	for i := len(e.Values) - 1; i >= 0; i-- {
		if !e.Values[i].BatchExit || e.Values[i].Code != "" {
			continue
		}
		// A step that already shows the whole command takes the result.
		if j := slices.IndexFunc(p.Values, func(v value) bool { return v.Label == "" && v.Code == command }); command != "" && j >= 0 {
			own, batch := &p.Values[j], p.Values[i]
			own.Duration, own.ExitCode, own.Running = batch.Duration, batch.ExitCode, batch.Running
			own.Tail, own.TailOmitted = batch.Tail, batch.TailOmitted
			p.Values = slices.Delete(p.Values, i, i+1)
			continue
		}
		if slices.ContainsFunc(e.Values, func(v activitypresentation.Value) bool { return !v.BatchExit }) {
			p.Values[i].Label = ""
		} else if command != "" {
			p.Values[i].Label, p.Values[i].Code, p.Values[i].Lang, p.Values[i].Fenced = "", command, "bash", true
		}
	}
	return p
}

func projectValues(values []activitypresentation.Value, summaries bool) []value {
	if len(values) == 0 {
		return nil
	}
	out := make([]value, len(values))
	for i, v := range values {
		p := value{
			DetailCode: v.DetailCode,
			Path:       v.Path, AutoReload: v.AutoReload, Approval: v.Approval,
			Kind: v.Kind, Verb: v.Verb, BatchExit: v.BatchExit, Label: v.Label, Code: v.Code, Lang: v.Lang, Fenced: v.Fenced, Body: v.Body,
			Subsumed: v.Subsumed, Effect: v.Effect, Pending: v.Pending, FoldSource: v.TemporarySource(), Duration: v.Duration.Milliseconds(), Thought: v.Thought.Milliseconds(), From: v.From, To: v.To,
			WaitReason: v.WaitReason, WaitTimeout: v.WaitTimeout,
			EditOutcome: v.EditOutcome, Running: v.Running, Live: v.Live, Tail: v.Tail, TailOmitted: v.TailOmitted,
		}
		if summaries {
			p.Summary = v.ReplySummary
		}
		if v.EditSource != "" {
			p.Label, p.Sources = v.EditLabel(), strings.Split(v.EditSource, ", ")
		}
		if v.ShowWorkdir {
			p.Workdir = v.Workdir
		}
		if v.Exited {
			p.ExitCode = new(v.ExitCode)
		}
		for _, r := range v.Reads {
			p.Reads = append(p.Reads, read{Path: r.Path, Ranges: r.Ranges})
		}
		for _, q := range v.Questions {
			p.Questions = append(p.Questions, question{Text: q.Text, Answer: q.Answer})
		}
		for _, w := range v.WaitTargets {
			p.Waits = append(p.Waits, wait{Name: w.Name, Status: w.Status})
		}
		for _, c := range v.Changes {
			p.Changes = append(p.Changes, change{Verb: c.Verb, Code: c.Code, Label: c.Label, Added: c.Added, Removed: c.Removed, Note: c.Note, Footer: c.Footer})
		}
		if h := v.Hook; h != nil {
			p.Hook = &hook{Status: h.Status, HasError: h.HasError}
		}
		out[i] = p
	}
	grouped := activitypresentation.GroupOperations(values)
	for start := 0; start < len(grouped); {
		n := activitypresentation.TemporaryEditRun(grouped[start:])
		if hidden := n - activitypresentation.ChangeRowsShown; hidden > 0 {
			out[start+activitypresentation.ChangeRowsShown].HiddenEdits = hidden
		}
		start += max(1, n)
	}
	return out
}

// projectRoster follows the terminal roster's facts: display names, the
// lifecycle state, work timers, usage and the lines each agent edited.
func projectRoster(p *activitypresentation.Publication) roster {
	r := roster{Agents: make([]agent, 0, len(p.Roster))}
	if n := p.NetCounts; n != nil && n.Added >= 0 && n.Removed >= 0 {
		r.Added, r.Removed = n.Added, n.Removed
	}
	for _, row := range p.Roster {
		a := row.Agent
		x := agent{
			Name: a.Name, Thread: a.Thread, Label: pathdisplay.AgentDisplayName(a.Name), Depth: row.Depth,
			Turns: a.Turns, Roundtrips: a.Roundtrips, Skills: a.Skills,
		}
		// Main's role is implied by its name.
		if a.Name != "/root" {
			x.Role = textsanitize.Safe(a.Role, false)
		}
		switch {
		case a.LatestKind == "error":
			x.State = "error"
		case !a.Responding && a.Final:
			x.State = "done"
		case !a.Responding:
			x.State = "idle"
		default:
			x.State = "working"
		}
		if !a.Started.IsZero() && a.WorkKnown {
			x.Work, x.WorkSince = new(a.WorkElapsed.Milliseconds()), millis(a.WorkSince)
			x.LastResponse = millis(a.LastResponse)
		}
		if a.CostKnown && a.Turns > 0 {
			x.Cost, x.CostPartial = new(a.Cost), a.CostPartial
		}
		if a.TokensKnown || a.InputTokens > 0 || a.OutputTokens > 0 {
			x.InputTokens, x.OutputTokens, x.UsagePartial = new(a.InputTokens), new(a.OutputTokens), a.UsagePartial
		}
		if a.ContextKnown {
			x.Context, x.Window = new(a.ContextTokens), a.ContextWindow
		}
		if lines, ok := p.LineCounts[a.Name]; ok && lines.Added >= 0 && lines.Removed >= 0 {
			x.Added, x.Removed = new(lines.Added), new(lines.Removed)
		}
		for _, item := range a.StatusItems {
			x.Items = append(x.Items, item.Text)
		}
		r.Agents = append(r.Agents, x)
	}
	return r
}
