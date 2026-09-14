package router

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/yusing/mekugi/internal/router/toolplugin"
)

func selectReadPage(ctx context.Context, manifest toolWorkerManifest, runtimeRoot, text string, maxTokens int) (string, error) {
	if len(text) <= maxTokens {
		return text, nil
	}
	end := min(len(text), maxTokens*128+utf8.UTFMax)
	for end < len(text) && !utf8.RuneStart(text[end]) {
		end--
	}
	formatted, err := toolplugin.FormatOutput(ctx, manifest.NodeExecutable, runtimeRoot,
		[]string{strconv.Itoa(maxTokens), "head", text[:end], ""})
	if err != nil {
		return "", err
	}
	if formatted.ExitCode != 0 || !strings.HasPrefix(text, formatted.Stdout) {
		return "", errors.New("read output selection failed")
	}
	if formatted.Stdout == "" && text != "" {
		return "", errors.New("token budget cannot admit the next character; increase --max-tokens")
	}
	return formatted.Stdout, nil
}
