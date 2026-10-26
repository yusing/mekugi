package terminalui

import (
	"fmt"
	"slices"
	"strings"
)

// ImageFrame is physical-terminal state. A caller commits it only after a
// successful write, retaining the previous state after delivery failure.
type ImageFrame struct {
	Placements            []ImagePlacement
	Protocol              ImageProtocol
	CellWidth, CellHeight int
}

func (f ImageFrame) Equal(next ImageFrame) bool {
	return f.Protocol == next.Protocol && f.CellWidth == next.CellWidth && f.CellHeight == next.CellHeight && slices.Equal(f.Placements, next.Placements)
}

func (f ImageFrame) Clear() string {
	var b strings.Builder
	for i, p := range f.Placements {
		if f.Protocol == KittyImages || f.Protocol == KittyFileImages {
			b.WriteString(DeleteImage(0xc100 + i))
		}
		for y := p.Y; y < p.Y+p.Rows; y++ {
			fmt.Fprintf(&b, "\x1b[%d;%dH\x1b[%dX", y+1, p.X+1, p.Columns)
		}
	}
	return b.String()
}

func (f ImageFrame) Paint() string {
	var b strings.Builder
	for i, p := range f.Placements {
		encoded := p.Encode(f.Protocol, f.CellWidth, f.CellHeight, 0xc100+i)
		if encoded != "" {
			fmt.Fprintf(&b, "\x1b7\x1b[%d;%dH%s\x1b8", p.Y+1, p.X+1, encoded)
		}
	}
	return b.String()
}
