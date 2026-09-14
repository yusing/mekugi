package router

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/yusing/mekugi/internal/router/toolplugin"
)

// The cursor binds a byte offset to an immutable selection. Binding includes
// stream identity when identical bytes can belong to different selections.
func readCursorOffset(text, cursor, binding string) (string, int, error) {
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(binding)))
	if cursor == "" {
		return digest, 0, nil
	}
	hash, number, ok := strings.Cut(cursor, ":")
	offset, err := strconv.Atoi(number)
	if !ok || hash != digest || err != nil || offset < 0 || offset >= len(text) ||
		strconv.Itoa(offset) != number || !utf8.RuneStart(text[offset]) {
		return "", 0, errors.New("invalid or stale read cursor; repeat the original read")
	}
	return digest, offset, nil
}

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
