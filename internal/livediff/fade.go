package livediff

import (
	"fmt"
	"strconv"
	"strings"
)

// RGB is a truecolor terminal color.
type RGB struct{ R, G, B uint8 }

// Canvas is what a fading row emerges from. A terminal-reported background
// replaces the theme's assumed one; the foreground stands in for default text.
type Canvas struct{ Background, Foreground RGB }

func (theme Theme) Canvas() Canvas {
	if theme == LightTheme {
		return Canvas{Background: RGB{255, 255, 255}, Foreground: RGB{31, 35, 40}}
	}
	return Canvas{Background: RGB{13, 17, 23}, Foreground: RGB{230, 237, 243}}
}

func mix(from, to RGB, progress float64) RGB {
	channel := func(a, b uint8) uint8 { return uint8(float64(a) + (float64(b)-float64(a))*progress + .5) }
	return RGB{channel(from.R, to.R), channel(from.G, to.G), channel(from.B, to.B)}
}

func parseRGB(params string) (RGB, bool) {
	fields := strings.Split(params, ";")
	if len(fields) != 3 {
		return RGB{}, false
	}
	var rgb [3]uint8
	for i, field := range fields {
		n, err := strconv.ParseUint(field, 10, 8)
		if err != nil {
			return RGB{}, false
		}
		rgb[i] = uint8(n)
	}
	return RGB{rgb[0], rgb[1], rgb[2]}, true
}

// Fade renders an SGR-colored row partway through appearing. Truecolor
// foregrounds and row fills blend from the canvas background, and default
// text takes the canvas foreground so it fades too. Other attributes and
// palette colors pass through. Progress 1 returns the row unchanged.
func Fade(line string, progress float64, canvas Canvas) string {
	if progress >= 1 {
		return line
	}
	progress = max(0, progress)
	fill := canvas.Background
	var out strings.Builder
	foreground := func(color RGB) {
		color = mix(fill, color, progress)
		fmt.Fprintf(&out, "\x1b[38;2;%d;%d;%dm", color.R, color.G, color.B)
	}
	foreground(canvas.Foreground)
	for line != "" {
		at := strings.Index(line, "\x1b[")
		if at < 0 {
			out.WriteString(line)
			break
		}
		out.WriteString(line[:at])
		line = line[at:]
		end := strings.IndexFunc(line[2:], func(r rune) bool { return r >= 0x40 && r <= 0x7e })
		if end < 0 {
			out.WriteString(line)
			break
		}
		sequence, params := line[:end+3], line[2:end+2]
		line = line[end+3:]
		if !strings.HasSuffix(sequence, "m") {
			out.WriteString(sequence)
			continue
		}
		switch {
		case params == "" || params == "0":
			out.WriteString(sequence)
			fill = canvas.Background
			foreground(canvas.Foreground)
		case params == "39":
			foreground(canvas.Foreground)
		case strings.HasPrefix(params, "38;2;"):
			if color, ok := parseRGB(params[5:]); ok {
				foreground(color)
			} else {
				out.WriteString(sequence)
			}
		case strings.HasPrefix(params, "48;2;"):
			if color, ok := parseRGB(params[5:]); ok {
				fill = mix(canvas.Background, color, progress)
				fmt.Fprintf(&out, "\x1b[48;2;%d;%d;%dm", fill.R, fill.G, fill.B)
			} else {
				out.WriteString(sequence)
			}
		default:
			out.WriteString(sequence)
		}
	}
	return out.String()
}
