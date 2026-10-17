package terminalview

import (
	"slices"

	"github.com/yusing/mekugi/libraries/textsanitize"
	"github.com/yusing/mekugi/modules/activity/presentation"
)

// CommandStream owns reveal debt only. Its inputs are detached admitted facts;
// it cannot change the command's canonical lifecycle or retained output.
// Source: apps/mekugi/internal/router/app_server_events.go:62:71@29a3a244;
// apps/mekugi/internal/router/exec_track.go:22:99@29a3a244.
type CommandStream struct {
	Output   CommandOutput
	Holding  bool
	segments []CommandOutput
	version  uint64
}

func NewCommandStream(text string) *CommandStream {
	return &CommandStream{Output: NewCommandOutput(instantOperations(text))}
}

func instantOperations(text string) bool {
	blocks := presentation.Operations(textsanitize.Safe(text, false))
	return len(blocks) > 0 && !slices.ContainsFunc(blocks, func(block presentation.Value) bool { return !block.Instant() })
}

func (p *CommandStream) RevealSegments(report presentation.SegmentReport, snapshot presentation.ReportSnapshot, version uint64, final bool) ([]OutputFrame, bool) {
	changed := p.version != version
	p.version = version
	for len(p.segments) < len(report.Segments) {
		p.segments = append(p.segments, NewCommandOutput(instantOperations(report.Segments[len(p.segments)].Text)))
	}
	for index, observed := range report.Segments {
		if !report.Lossy && observed.Output != "" {
			p.segments[index].Write(observed.Output)
		}
	}
	frames := make([]OutputFrame, len(snapshot.Segments))
	for index := range snapshot.Segments {
		frame, advanced := p.segments[index].Advance()
		changed = changed || advanced
		if final && snapshot.SeparateOutput {
			frame = p.segments[index].Settle()
		}
		frames[index] = frame
	}
	return frames, changed
}
