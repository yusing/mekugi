package router

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/yusing/mekugi/libraries/outputbudget"
)

func selectReadPage(ctx context.Context, text string, maxTokens int) (string, error) {
	if len(text) <= maxTokens {
		return text, nil
	}
	end := min(len(text), maxTokens*128+utf8.UTFMax)
	for end < len(text) && !utf8.RuneStart(text[end]) {
		end--
	}
	formatted, err := outputbudget.Select(ctx, maxTokens, "head", text[:end], "")
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(text, formatted.Stdout) {
		return "", errors.New("read output selection failed")
	}
	if len(formatted.Stdout) < len(text) {
		end := strings.LastIndexByte(formatted.Stdout, '\n') + 1
		formatted.Stdout = formatted.Stdout[:end]
	}
	if formatted.Stdout == "" && text != "" {
		return "", errors.New("token budget cannot admit the next complete row; increase --max-tokens")
	}
	return formatted.Stdout, nil
}
