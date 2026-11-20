package terminalview

import (
	"strings"

	"github.com/yusing/mekugi/modules/activity/presentation"

	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/textsanitize"

	"github.com/yusing/mekugi/libraries/terminalui"
)

// ErrorHasDetails reports content omitted by the inline first-line preview.
func ErrorHasDetails(block Block) bool {
	return strings.TrimSpace(textsanitize.Safe(block.Body, false)) != presentation.ErrorPreview(block.Body)
}

// ErrorRows adds a dialog hint only when the preview omits retained content.
func ErrorRows(theme diffrender.Theme, block Block, width int) []string {
	rows := terminalui.Wrap(theme.Danger()+presentation.ErrorPreview(block.Body)+terminalui.Reset, width, false)
	if !ErrorHasDetails(block) {
		return rows
	}
	hint := "details · ctrl+b !"
	if block.Hovered {
		hint = Underline(hint)
	}
	return append(rows, terminalui.Wrap(terminalui.Dim+hint+terminalui.Undim, width, false)...)
}
