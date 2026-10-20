package codexbinding

import (
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestLiveDiffPreviewPacerIsSteadyAndBounded(t *testing.T) {
	t.Parallel()
	var pacer liveDiffPreviewPacer
	input := ""
	shown := 0
	// Bursts every fourth frame reveal whole lines on most frames, never all at once.
	steps := 0
	for frame := range 40 {
		if frame%4 == 0 {
			input += strings.Repeat("界x", 6) + "\n" + strings.Repeat("y", 10) + "\n"
		}
		next := pacer.advance(input, 1, false, false)
		if next < shown || next > len(input) || next > 0 && input[next-1] != '\n' {
			t.Fatalf("frame %d revealed %d of %d after %d", frame, next, len(input), shown)
		}
		if frame > 8 && frame%4 == 0 && next == len(input) {
			t.Fatalf("frame %d did not pace the burst: %d -> %d of %d", frame, shown, next, len(input))
		}
		if next > shown {
			steps++
		}
		shown = next
	}
	if steps < 16 {
		t.Fatalf("bursts were not revealed line by line: %d steps", steps)
	}
	// A large backlog skips to the window at the tip.
	input += strings.Repeat("y\n", 32<<10)
	if next := pacer.advance(input, 1, false, false); next < len(input)-liveDiffPreviewMaxLag {
		t.Fatalf("lag exceeded its window: %d of %d", next, len(input))
	}
	// A finished call converges promptly, including an unterminated line.
	input += "tail"
	for range 20 {
		shown = pacer.advance(input, 1, true, false)
	}
	if shown != len(input) {
		t.Fatalf("final input not reached: %d of %d", shown, len(input))
	}
}

func TestLiveDiffPreviewPacerKeepsUpAcrossHeldFrames(t *testing.T) {
	t.Parallel()
	// A reveal held between target units spans several frames. The pacer must
	// cover what arrived meanwhile rather than one unit per call, or the
	// backlog grows until completion releases it at once.
	var pacer liveDiffPreviewPacer
	line := strings.Repeat("x", 39) + "\n"
	input := ""
	for step := range 40 {
		input += strings.Repeat(line, 5)
		shown := pacer.advance(input, 8, false, false)
		if step > 8 && len(input)-shown > 10*len(line) {
			t.Fatalf("step %d fell behind: %d of %d", step, shown, len(input))
		}
	}
	// Idle frames are not arrival time: a burst after a stall is still paced.
	pacer = liveDiffPreviewPacer{}
	burst := strings.Repeat(line, 40) // Within the lag window.
	if shown := pacer.advance(burst, 100, false, false); shown == 0 || shown > len(burst)/4 {
		t.Fatalf("burst after idle was not paced: %d of %d", shown, len(burst))
	}
	for pacer.cursor < len(burst) {
		pacer.advance(burst, 1, false, false)
	}
	before := pacer.shown
	want := min(8*len(line), len(burst)-before)
	if shown := pacer.advance(burst, 8, false, false); shown-before != want {
		t.Fatalf("received source backlog stalled across held frames: advanced %d bytes, want %d", shown-before, want)
	}
}

func TestLiveDiffPreviewPacerBuffersUnits(t *testing.T) {
	t.Parallel()
	// The pacer offers candidate boundaries; decoded target syntax decides
	// which candidates may actually be displayed.
	reveal := func(input string, encoded bool, step int) []string {
		var pacer liveDiffPreviewPacer
		var shown []string
		for i := min(step, len(input)); ; i = min(i+step, len(input)) {
			if next := pacer.advance(input[:i], 1, false, encoded); len(shown) == 0 && next > 0 || len(shown) > 0 && input[:next] != shown[len(shown)-1] {
				shown = append(shown, input[:next])
			}
			if i == len(input) {
				return shown
			}
		}
	}
	// Semicolons are candidates even inside a for header. The source gate
	// must reject these, rather than making transport pacing language-aware.
	body := "for (i = 0; i < n; i++) {\n\tx();\n"
	if got := reveal(body, false, 3); !slices.Equal(got, []string{"for (i = 0;", "for (i = 0; i < n;", "for (i = 0; i < n; i++) {\n", strings.TrimSuffix(body, "\n")}) {
		t.Fatalf("lines: %q", got)
	}
	// Nested escapes are also candidates; projection decodes their meaning.
	encoded := `{"cmd":"cat > f <<'EOF'\nsay \\n here\nnext`
	if got := reveal(encoded, true, 3); !slices.Equal(got, []string{
		`{"cmd":"cat > f <<'EOF'\n`, `{"cmd":"cat > f <<'EOF'\nsay \\n`, `{"cmd":"cat > f <<'EOF'\nsay \\n here\n`,
	}) {
		t.Fatalf("encoded: %q", got)
	}
	// A stalled line stays buffered regardless of elapsed frames.
	var pacer liveDiffPreviewPacer
	long := strings.Repeat("z", 64)
	shown := 0
	for range 100 {
		shown = pacer.advance(long, 1, false, false)
	}
	if shown != 0 {
		t.Fatalf("unfinished line was released: %d of %d", shown, len(long))
	}
}

func TestLiveDiffPreviewPacerKeepsEscapesWhole(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		input string
		want  int
	}{
		{`"a\`, 2}, {`"a\\`, 4}, {`"a\u00`, 2}, {`"a\u003e`, 8}, {`"a\x4`, 2}, {`"a\n`, 4},
	} {
		if got := liveDiffEscapeEnd(test.input, len(test.input)); got != test.want {
			t.Errorf("%q: cut at %d, want %d", test.input, got, test.want)
		}
	}
	var pacer liveDiffPreviewPacer
	input := `"a\`
	// An unfinished encoded line stays buffered.
	shown := 0
	for range 100 {
		shown = pacer.advance(input, 1, false, true)
	}
	if shown != 0 {
		t.Fatalf("streaming reveal split an escape at %d", shown)
	}
	// Finished input is shown whole even when it ends in a backslash.
	for range 3 {
		pacer.advance(input, 1, true, true)
	}
	if pacer.shown != len(input) {
		t.Fatalf("finished input stalled at %d of %d", pacer.shown, len(input))
	}
}

func TestLiveDiffBurstRevealsOneLinePerFrame(t *testing.T) {
	for _, encoded := range []bool{false, true} {
		input := strings.Repeat("line\n", 20)
		if encoded {
			input = strconv.Quote(input)
		}
		var pacer liveDiffPreviewPacer
		previous := 0
		for range 200 {
			next := pacer.advance(input, 1, false, encoded)
			if next > previous {
				unit := input[previous:next]
				separator := "\n"
				if encoded {
					separator = `\n`
				}
				if strings.Count(unit, separator) != 1 {
					t.Fatalf("burst emitted more than one line: %q", unit)
				}
			}
			previous = next
		}
		want := len(input)
		if encoded {
			want-- // Closing quote is envelope, not another source line.
		}
		if previous != want {
			t.Fatalf("burst stopped at %d, want %d", previous, want)
		}
	}
}
