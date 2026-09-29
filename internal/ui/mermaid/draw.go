package mermaid

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Render returns a complete flowchart, or false so the caller can retain source.
// Source: codex-rs/mermaid/src/draw.rs:16:260@[687a119f0fcaace47e1f1abcc77cec6c813fd6da] render
func Render(source string, maxWidth int) ([]string, bool) {
	g, ok := parse(source)
	if !ok {
		return nil, false
	}
	horizontal := g.direction == "LR" || g.direction == "RL"
	labels := make([]string, len(g.nodes))
	boxCross := 3
	labelWidth := 0
	for i, n := range g.nodes {
		labels[i] = n.label
		if n.shape == "decision" {
			labels[i] = "◇ " + n.label
		}
		if !horizontal {
			boxCross = max(boxCross, ansi.StringWidth(labels[i])+4)
		}
	}
	for _, e := range g.edges {
		labelWidth = max(labelWidth, ansi.StringWidth(e.label))
	}
	counts := make([]int, len(g.nodes))
	ports := make([][2]int, len(g.edges))
	for i, e := range g.edges {
		ports[i][0] = counts[e.from]
		counts[e.from]++
		ports[i][1] = counts[e.to]
		counts[e.to]++
	}
	stride := 1
	if horizontal {
		stride = labelWidth + 2
	}
	sizes := make([]int, len(g.nodes))
	starts := make([]int, len(g.nodes))
	along := 0
	for i := range sizes {
		sizes[i] = counts[i] + 3
		if horizontal {
			sizes[i] = max(ansi.StringWidth(labels[i])+4, counts[i]*stride+2)
		}
	}
	for i := range starts {
		j := i
		if g.direction == "BT" || g.direction == "RL" {
			j = len(starts) - 1 - i
		}
		starts[j] = along
		along += sizes[j] + 2
	}
	firstLane := boxCross + labelWidth + 5
	if horizontal {
		firstLane = boxCross + 4
	}
	across := boxCross
	if len(g.edges) > 0 {
		across = firstLane + len(g.edges)*2 - 1
	}
	width, height := across, along-2
	if horizontal {
		width, height = along-2, across
	}
	if width > maxWidth || width*height > maxCells {
		return nil, false
	}
	cells := make([][]rune, height)
	for y := range cells {
		cells[y] = []rune(strings.Repeat(" ", width))
	}
	set := func(x, y int, c rune) {
		if horizontal {
			cells[x][y] = transpose(c)
		} else {
			cells[y][x] = c
		}
	}
	get := func(x, y int) rune {
		if horizontal {
			return transpose(cells[x][y])
		}
		return cells[y][x]
	}
	put := func(x, y int, text string) {
		for _, c := range text {
			w := ansi.StringWidth(string(c))
			cells[y][x] = c
			for j := 1; j < w; j++ {
				cells[y][x+j] = 0
			}
			x += w
		}
	}
	for i, n := range g.nodes {
		start, end := starts[i], starts[i]+sizes[i]-1
		corners := []rune("┌┐└┘")
		if n.shape == "stadium" {
			corners = []rune("╭╮╰╯")
		}
		set(0, start, corners[0])
		set(boxCross-1, start, corners[1])
		set(0, end, corners[2])
		set(boxCross-1, end, corners[3])
		for x := 1; x < boxCross-1; x++ {
			set(x, start, '─')
			set(x, end, '─')
		}
		for y := start + 1; y < end; y++ {
			set(0, y, '│')
			set(boxCross-1, y, '│')
		}
		if horizontal {
			put(start+2, 1, labels[i])
		} else {
			put(2, start+1, labels[i])
		}
	}
	endpoints := make([][2]int, len(g.edges))
	for i, e := range g.edges {
		offset := func(n, p int) int {
			if horizontal {
				return starts[n] + 1 + p*stride
			}
			return starts[n] + 2 + p
		}
		endpoints[i] = [2]int{offset(e.from, ports[i][0]), offset(e.to, ports[i][1])}
	}
	for i, e := range g.edges {
		a, b := endpoints[i][0], endpoints[i][1]
		c := '│'
		if e.dashed {
			c = '┆'
		}
		for y := min(a, b) + 1; y < max(a, b); y++ {
			set(firstLane+2*i, y, c)
		}
	}
	for i, e := range g.edges {
		a, b := endpoints[i][0], endpoints[i][1]
		lane := firstLane + 2*i
		for _, y := range []int{a, b} {
			for x := boxCross; x < lane; x++ {
				c := '─'
				if e.dashed {
					c = '┄'
				}
				if prev := get(x, y); prev == '│' || prev == '┆' {
					c = '╪'
				}
				set(x, y, c)
			}
		}
		set(boxCross-1, a, '├')
		set(boxCross-1, b, '├')
		set(boxCross, a, e.source)
		set(boxCross, b, e.target)
		if a < b {
			set(lane, a, '┐')
			set(lane, b, '┘')
		} else {
			set(lane, a, '┘')
			set(lane, b, '┐')
		}
		if horizontal {
			put(a+1, boxCross+1, e.label)
		} else {
			put(boxCross+2, a, e.label)
		}
	}
	lines := make([]string, height)
	for y, row := range cells {
		var b strings.Builder
		for _, c := range row {
			if c != 0 {
				b.WriteRune(c)
			}
		}
		lines[y] = strings.TrimRight(b.String(), " ")
	}
	return lines, true
}

func transpose(c rune) rune {
	switch c {
	case '─':
		return '│'
	case '│':
		return '─'
	case '┄':
		return '┆'
	case '┆':
		return '┄'
	case '┐':
		return '└'
	case '└':
		return '┐'
	case '╮':
		return '╰'
	case '╰':
		return '╮'
	case '├':
		return '┬'
	case '┬':
		return '├'
	case '◄':
		return '▲'
	case '▲':
		return '◄'
	}
	return c
}
