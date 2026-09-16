package router

import (
	"context"
	"fmt"
	"io"

	codexinstructions "github.com/yusing/mekugi/contrib/codex"
	"mvdan.cc/sh/v3/interp"
)

func executeHHelp(ctx context.Context, arguments []string) error {
	handler := interp.HandlerCtx(ctx)
	if len(arguments) > 1 {
		_, _ = fmt.Fprintln(handler.Stderr, "hhelp: expected zero or one topic; run hhelp to list topics")
		return interp.ExitStatus(2)
	}
	topic := ""
	if len(arguments) == 1 {
		topic = arguments[0]
	}
	content, ok := codexinstructions.Help(topic)
	if !ok {
		_, _ = fmt.Fprintln(handler.Stderr, "hhelp: unknown topic; run hhelp to list topics")
		return interp.ExitStatus(2)
	}
	_, err := io.WriteString(handler.Stdout, content)
	return err
}
