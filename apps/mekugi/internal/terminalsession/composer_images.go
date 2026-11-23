package terminalsession

import (
	"fmt"
	"slices"
	"strings"

	"github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

const (
	composerThumbMaxCols      = 10
	composerThumbMaxRows      = 3
	composerThumbFallbackCols = 6
	composerThumbGap          = 1
)

type composerImageStrip struct {
	rows       []string
	demands    []terminalui.ImageSource
	placements []terminalui.ImagePlacement
}

// renderComposerImages lays out a horizontal strip of bordered thumbnail cards
// for images attached in the composer draft. Terminals supporting Kitty or Sixel
// graphics receive native ImagePlacements; terminals without graphics protocols
// (including tmux, zellij, and 24-bit color terminals) render Unicode half-block
// thumbnails inline.
func renderComposerImages(
	images []composer.Token,
	previews map[terminalui.ImageSource]*terminalui.ImagePreview,
	protocol terminalui.ImageProtocol,
	cellWidth, cellHeight int,
	indent, textWidth, maxRows int,
) composerImageStrip {
	if len(images) == 0 || textWidth < 6 || maxRows < 4 {
		return composerImageStrip{}
	}
	blankTop := maxRows >= 5
	innerRows := 2
	if maxRows >= 6 {
		innerRows = composerThumbMaxRows
	}
	cardHeight := innerRows + 2
	bodyStartY := 1
	if blankTop {
		bodyStartY = 2
	}

	type card struct {
		lines []string
		width int
	}

	var out composerImageStrip
	var cards []card
	usedWidth := 0
	overflow := 0

	for i, token := range images {
		if token.Path == "" {
			continue
		}
		source := terminalui.ImageSource{Path: token.Path, Composer: true}
		preview, known := previews[source]
		if !known && !slices.Contains(out.demands, source) {
			out.demands = append(out.demands, source)
		}

		label := fmt.Sprintf("#%d", i+1)
		useNative := protocol != terminalui.NoImages
		cols, rows := 0, 0
		if preview != nil {
			cols, rows = preview.ThumbnailSize(composerThumbMaxCols, innerRows, protocol, cellWidth, cellHeight)
			if cols == 0 || rows == 0 {
				useNative = false
				cols, rows = preview.ThumbnailSize(composerThumbMaxCols, innerRows, terminalui.NoImages, 0, 0)
			}
		}
		innerWidth := max(composerThumbFallbackCols, len(label)+2)
		if cols > 0 {
			innerWidth = max(cols, len(label)+2)
		}
		cardWidth := innerWidth + 2
		gap := 0
		if len(cards) > 0 {
			gap = composerThumbGap
		}
		remainingAfter := len(images) - i - 1
		reserveOverflow := 0
		if remainingAfter > 0 {
			reserveOverflow = len(fmt.Sprintf(" +%d", remainingAfter))
		}
		if usedWidth+gap+cardWidth > textWidth || (len(cards) > 0 && remainingAfter > 0 && usedWidth+gap+cardWidth+reserveOverflow > textWidth) {
			overflow = len(images) - i
			break
		}

		cardX := indent + usedWidth + gap
		usedWidth += gap + cardWidth

		lines := make([]string, cardHeight)
		lines[0] = composerCardTop(label, innerWidth)
		lines[cardHeight-1] = terminalui.Dim + "╰" + strings.Repeat("─", innerWidth) + "╯" + terminalui.Reset

		var halfBlocks []string
		if preview != nil && cols > 0 && rows > 0 {
			if useNative {
				padLeft := (innerWidth - cols) / 2
				padTop := (innerRows - rows) / 2
				out.placements = append(out.placements, terminalui.ImagePlacement{
					Image:     preview,
					X:         cardX + 1 + padLeft,
					Y:         bodyStartY + padTop,
					Columns:   cols,
					Rows:      rows,
					First:     0,
					TotalRows: rows,
				})
			} else {
				halfBlocks = preview.HalfBlocks(cols, rows)
			}
		}

		padTop := (innerRows - rows) / 2
		padLeft := (innerWidth - cols) / 2
		padRight := max(0, innerWidth-cols-padLeft)
		midRow := innerRows / 2
		for r := range innerRows {
			var body string
			switch {
			case len(halfBlocks) > 0 && r >= padTop && r < padTop+len(halfBlocks):
				body = strings.Repeat(" ", padLeft) + halfBlocks[r-padTop] + strings.Repeat(" ", padRight)
			case preview != nil && useNative:
				body = strings.Repeat(" ", innerWidth)
			case !known && r == midRow:
				body = centerDimLabel("…", innerWidth)
			case known && preview == nil && r == midRow:
				body = centerDimLabel("img", innerWidth)
			default:
				body = strings.Repeat(" ", innerWidth)
			}
			lines[1+r] = terminalui.Dim + "│" + terminalui.Reset + body + terminalui.Dim + "│" + terminalui.Reset
		}
		cards = append(cards, card{lines: lines, width: cardWidth})
	}

	if len(cards) == 0 {
		return out
	}

	totalRows := cardHeight
	if blankTop {
		totalRows++
	}
	out.rows = make([]string, totalRows)
	rowOffset := 0
	if blankTop {
		out.rows[0] = ""
		rowOffset = 1
	}
	prefix := strings.Repeat(" ", indent)
	sep := strings.Repeat(" ", composerThumbGap)
	midCardRow := cardHeight / 2
	for r := range cardHeight {
		var b strings.Builder
		b.WriteString(prefix)
		for i, c := range cards {
			if i > 0 {
				b.WriteString(sep)
			}
			b.WriteString(c.lines[r])
		}
		if overflow > 0 && r == midCardRow {
			fmt.Fprintf(&b, " %s+%d%s", terminalui.Dim, overflow, terminalui.Reset)
		}
		out.rows[rowOffset+r] = b.String()
	}
	return out
}

func composerCardTop(label string, innerWidth int) string {
	if len(label)+2 > innerWidth {
		return terminalui.Dim + "╭" + strings.Repeat("─", innerWidth) + "╮" + terminalui.Reset
	}
	right := innerWidth - 1 - len(label)
	return terminalui.Dim + "╭─" + label + strings.Repeat("─", right) + "╮" + terminalui.Reset
}

func centerDimLabel(label string, width int) string {
	labelWidth := len(label)
	if label == "…" {
		labelWidth = 1
	}
	if labelWidth >= width {
		return terminalui.Dim + label + terminalui.Reset
	}
	left := (width - labelWidth) / 2
	right := width - labelWidth - left
	return strings.Repeat(" ", left) + terminalui.Dim + label + terminalui.Reset + strings.Repeat(" ", right)
}
