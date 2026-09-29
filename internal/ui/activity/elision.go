package activity

import (
	"fmt"
	"strconv"
)

// Elision counts content a row leaves out of view. Every hint renders through
// it, so counts read alike wherever content is clipped, and each underlines
// alike under the pointer that opens its content.
type Elision struct {
	Hidden  int
	Unit    string // Counted noun used as written; empty counts lines.
	Form    ElisionForm
	Hovered bool
}

type ElisionForm uint8

const (
	// ElisionMore ends clipped rows: "… +N lines".
	ElisionMore ElisionForm = iota
	// ElisionSuffix follows text on its row: "+N lines".
	ElisionSuffix
	// ElisionEarlier precedes the rows shown: "… N earlier lines".
	ElisionEarlier
	// ElisionLead counts earlier lines in a verb column: "+N".
	ElisionLead
	// ElisionContent counts collapsed content in full: "(N lines)".
	ElisionContent
)

// Text is the hint without styling, or "" when nothing is hidden.
func (e Elision) Text() string {
	if e.Hidden <= 0 {
		return ""
	}
	count := lineCount
	if e.Unit != "" {
		count = func(n int) string { return strconv.Itoa(n) + " " + e.Unit }
	}
	switch e.Form {
	case ElisionSuffix:
		return "+" + count(e.Hidden)
	case ElisionEarlier:
		if e.Hidden == 1 {
			return "… 1 earlier line"
		}
		return fmt.Sprintf("… %d earlier lines", e.Hidden)
	case ElisionLead:
		return "+" + strconv.Itoa(e.Hidden)
	case ElisionContent:
		return "(" + count(e.Hidden) + ")"
	}
	return "… +" + count(e.Hidden)
}

// String is the muted hint, underlined while hovered.
func (e Elision) String() string {
	text := e.Text()
	if text == "" {
		return ""
	}
	if e.Hovered {
		text = Underline(text)
	}
	return Dim + text + Undim
}

// lineCount labels n lines.
func lineCount(n int) string {
	if n == 1 {
		return "1 line"
	}
	return fmt.Sprintf("%d lines", n)
}
