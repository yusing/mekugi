package router

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

func openCodeSessionID(provider, thread string) string {
	if strings.TrimSpace(thread) == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(provider + "\x00" + strings.TrimSpace(thread)))
	return "mekugi-" + hex.EncodeToString(sum[:])
}

func isOpenCodeModel(model string) bool {
	return strings.HasPrefix(model, "opencode-go:") || strings.HasPrefix(model, "opencode-zen:")
}

func isChatCompletionsModel(model string) bool {
	return isGrokModel(model) || isOpenCodeModel(model)
}
