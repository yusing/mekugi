package router

import (
	"github.com/yusing/mekugi/providers/grok"
	"github.com/yusing/mekugi/providers/opencode"
)

func isChatCompletionsModel(model string) bool {
	return grok.IsModel(model) || opencode.IsModel(model)
}
