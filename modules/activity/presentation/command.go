package presentation

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/libraries/shellinstrumentation"
	"github.com/yusing/mekugi/libraries/textsanitize"
	"github.com/yusing/mekugi/modules/activity/feed"
	"mvdan.cc/sh/v3/syntax"
)

// Command is an admitted invocation snapshot shared by feed and detail projection.
// Script keeps shell structure; Display has authenticated instrumentation removed.
// Output snapshots carry retained facts, never process or storage handles.
type Command struct {
	Source                           uint64
	Script, Display, Directory, Text string
	Observed, Started, Ended         time.Time
	Duration                         time.Duration
	Running, Exited                  bool
	ExitCode                         int
	Approval                         string
	Tail                             []string
	TailOmitted                      int
	Output                           *feed.OutputView
	OutputContent                    bool
	Changes                          []ChangeRow
	Segments                         []CommandSegment
	Captured                         bool
	CaptureText                      string
}

type CommandSegment struct {
	Source, Text     string
	Running, Skipped bool
	ExitCode         int
	Started, Ended   time.Time
	Duration         time.Duration
	Tail             []string
	TailOmitted      int
	Output           *feed.OutputView
	OutputContent    bool
	Changes          []ChangeRow
	Writer           bool
}

// Operations parses one invocation, merging adjacent read targets without
// turning those targets into separately navigable invocations.
// Source: apps/mekugi/internal/router/live_activity_blocks.go:224:239@6d403a5a.
func Operations(text string) []Value {
	content := feed.ParseOperations(text)
	values := make([]Value, len(content))
	for i, value := range content {
		values[i] = Value{Content: value, Operation: i + 1}
	}
	var merged []Value
	for i := 0; i < len(values); {
		first, size := TargetRun(values[i:], nil)
		// Parsed targets belong to one invocation, not separate dialog pages.
		first.Members = nil
		merged = append(merged, first)
		i += size
	}
	return merged
}

// Compound reports whether invocation timing must stay on a combined result.
// Source: apps/mekugi/internal/router/live_activity_blocks.go:52:80@6d403a5a.
func (c Command) Compound(values []Value) bool {
	compound := len(values) > 1 || len(c.Segments) > 0
	if compound || c.Script == "" {
		return compound
	}
	if program, err := syntax.NewParser().Parse(strings.NewReader(c.Script), ""); err == nil {
		if len(values) == 1 && values[0].Verb == "Run" && values[0].Code == textsanitize.Safe(c.Display, false) && shellinstrumentation.HasShellSetup(program) {
			return false
		}
		compound = len(program.Stmts) > 1
		if len(program.Stmts) == 1 {
			if binary, ok := program.Stmts[0].Cmd.(*syntax.BinaryCmd); ok {
				compound = binary.Op == syntax.AndStmt || binary.Op == syntax.OrStmt
			}
		}
	}
	return compound
}

// ApplyFacts keeps host timing and exit facts off inferred individual operations.
// Source: apps/mekugi/internal/router/live_activity_blocks.go:29:50@6d403a5a.
func (c Command) ApplyFacts(values []Value) {
	compound := c.Compound(values)
	for i := range values {
		value := &values[i]
		if (value.Kind != "op" && value.Kind != "reads") || value.Skipped || compound && !value.BatchExit {
			continue
		}
		if c.Exited {
			value.ExitCode, value.Exited = c.ExitCode, true
		}
		value.Duration, value.Started, value.Ended = c.Duration, c.Started, c.Ended
		value.NotificationTiming = true
		if c.Running && value.Started.IsZero() {
			value.Started = c.Observed
		}
	}
}

func outputID(output *feed.OutputView, id int) int {
	if output == nil {
		return 0
	}
	return id
}

func hasOutput(output *feed.OutputView, content bool) bool {
	return output != nil && (content || output.Dropped > 0 || output.Released)
}

// SegmentValues uses observed boundaries, retaining output and honest outcomes
// after captured edits replace intent. Detail values additionally retain source.
// Source: apps/mekugi/internal/router/live_activity_blocks.go:309:421@6d403a5a.
func (c Command) SegmentValues(detail bool) []Value {
	var values []Value
	if c.CaptureText != "" {
		values = append(values, Operations(textsanitize.Safe(c.CaptureText, false))...)
	}
	edited := func(paths []string, source string) Value {
		value := Value{Kind: "op", Verb: "Edited", Label: strings.Join(paths, ", ")}
		if detail {
			value.Code, value.Lang, value.Fenced = textsanitize.Safe(source, false), "bash", true
		}
		return value
	}
	_, classified := ClassifyShell(c.Script, "")
	for segmentIndex, segment := range c.Segments {
		if classified && !segment.Skipped && segment.ExitCode == 0 && ShellSeparator(segment.Source) {
			continue
		}
		operations := Operations(textsanitize.Safe(segment.Text, false))
		if c.Captured && !segment.Running && !segment.Skipped && segment.ExitCode == 0 {
			var paths []string
			operations = slices.DeleteFunc(operations, func(value Value) bool {
				edit := slices.Contains([]string{"Edit", "Create", "Delete", "Move"}, value.Verb)
				if path := value.EditPath(); edit && path != "" {
					paths = append(paths, "`"+path+"`")
				}
				return edit || segment.Writer && value.Verb == "Run"
			})
			if len(operations) == 0 && (detail && (len(paths) > 0 || segment.Writer) || len(segment.Tail) > 0 || segment.TailOmitted > 0 || hasOutput(segment.Output, segment.OutputContent)) {
				operations = []Value{edited(paths, segment.Source)}
			}
		}
		for i := range operations {
			operations[i].Running, operations[i].Skipped = segment.Running, segment.Skipped
			if value := &operations[i]; !segment.Running && strings.HasSuffix(value.EditSource, " (requested)") {
				status := "ran"
				if segment.Skipped {
					status = "skipped"
				} else if segment.ExitCode != 0 {
					status = "failed"
				}
				value.EditSource = strings.TrimSuffix(value.EditSource, " (requested)")
				value.Label = strings.TrimSuffix(value.Label, " (requested)")
				value.EditOutcome = status
			}
		}
		if len(operations) == 0 {
			continue
		}
		last := &operations[len(operations)-1]
		if detail && last.EditSource != "" {
			last.Code, last.Lang, last.Fenced = textsanitize.Safe(segment.Source, false), "bash", true
		}
		last.ExitCode, last.Segment = segment.ExitCode, true
		last.Operation = segmentIndex + 1
		last.Exited = !segment.Running && !segment.Skipped
		if !segment.Skipped {
			last.Started, last.Ended, last.Duration = segment.Started, segment.Ended, segment.Duration
		}
		last.Tail, last.TailOmitted, last.Output = segment.Tail, segment.TailOmitted, outputID(segment.Output, segmentIndex+2)
		last.Changes = segment.Changes
		values = append(values, operations...)
	}
	combinedOutput := c.Output != nil && slices.ContainsFunc(c.Segments, func(segment CommandSegment) bool { return !segment.Skipped && segment.Output == nil })
	combined := combinedOutput || len(c.Tail) > 0 || c.TailOmitted > 0 || len(values) == 0 && hasOutput(c.Output, c.OutputContent)
	if combined {
		if len(values) == 0 && c.Captured {
			values = append(values, edited(nil, c.Script))
		} else if len(values) == 0 {
			values = append(values, Value{Kind: "op", Verb: "Run", Code: textsanitize.Safe(c.Script, false), Lang: "bash", Fenced: true})
		}
		for i := len(values) - 1; i >= 0; i-- {
			if !values[i].Skipped {
				values[i].Tail, values[i].TailOmitted, values[i].Output = c.Tail, c.TailOmitted, outputID(c.Output, 1)
				values[i].Changes = c.Changes
				break
			}
		}
	}
	return FoldStages(values)
}

// DetailValues keeps one combined page unless retained segment boundaries can
// attribute every operation's output. Output text never manufactures boundaries.
// Source: apps/mekugi/internal/router/output_dialog.go:189:254@6d403a5a.
func (c Command) DetailValues() []Value {
	if c.Source == 0 || c.Script == "" && len(c.Segments) == 0 {
		return nil
	}
	separate := len(c.Segments) == 1 || len(c.Segments) > 1 && len(c.Tail) == 0
	for _, segment := range c.Segments {
		if len(c.Segments) > 1 && !segment.Skipped && segment.Output == nil {
			separate = false
		}
	}
	var values []Value
	if !separate {
		operations := Operations(c.Text)
		if len(c.Segments) < 2 && len(operations) < 2 && (len(operations) == 0 || len(operations[0].Reads) < 2) {
			return nil
		}
		values = []Value{{Source: c.Source, Kind: "op", Verb: "Run", Label: "combined output", BatchExit: true, Code: c.Display, Lang: "bash", Output: outputID(c.Output, 1), Tail: c.Tail, TailOmitted: c.TailOmitted, Running: c.Running, Body: "Per-command output boundaries were not retained for this invocation."}}
		classified := Operations(ShellSource(c.Display, c.Directory))
		if slices.ContainsFunc(classified, func(value Value) bool { return value.EditSource != "" }) {
			if segments, ok := shellinstrumentation.Split(c.Display); ok {
				classified = nil
				for _, segment := range segments {
					text := ShellSource(segment.Source, c.Directory)
					if strings.TrimSpace(text) == "" {
						text = RunSource(segment.Source)
					}
					classified = append(classified, Operations(text)...)
				}
			}
			for i := range classified {
				classified[i].Requested = true
				if classified[i].Verb == "Run" {
					classified[i].Label += " · requested"
				}
			}
			values[0].RequestedOperations = classified
		}
	} else {
		for _, grouped := range GroupOperations(c.SegmentValues(true)) {
			for _, value := range Originals(grouped) {
				if value.Segment {
					value.Source = c.Source
					values = append(values, value)
				}
			}
		}
		slices.SortStableFunc(values, func(a, b Value) int { return cmp.Compare(a.Operation, b.Operation) })
	}
	c.ApplyFacts(values)
	if len(values) > 0 {
		values[0].Approval = c.Approval
	}
	return values
}
