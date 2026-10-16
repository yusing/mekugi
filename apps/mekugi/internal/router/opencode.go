package router

import (
	"strings"
)

func isOpenCodeModel(model string) bool {
	return strings.HasPrefix(model, "opencode-go:") || strings.HasPrefix(model, "opencode-zen:")
}

func isChatCompletionsModel(model string) bool {
	return isGrokModel(model) || isOpenCodeModel(model)
}
