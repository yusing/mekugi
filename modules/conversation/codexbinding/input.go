// Source: apps/mekugi/internal/router/app_server_clipboard.go:196:230@[e9da429a] draft-to-host input mapping.
package codexbinding

import (
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

func Input(d composer.Draft) []map[string]any {
	var input []map[string]any
	appendText := func(start, end int) {
		var elements []binding.InputTextElement
		for _, span := range d.Spans() {
			if span.Kind != composer.Image && span.Start >= start && span.End <= end {
				elements = append(elements, binding.InputTextElement{
					ByteRange:   binding.InputByteRange{Start: span.Start - start, End: span.End - start},
					Placeholder: d.Text[span.Start:span.End],
				})
			}
		}
		input = append(input, binding.TextInput(d.Text[start:end], elements))
	}
	at := 0
	for _, attachment := range d.Images {
		if attachment.Start > at {
			appendText(at, attachment.Start)
		}
		input = append(input, binding.ImageInput(attachment.Path))
		at = attachment.End
	}
	if at < len(d.Text) || len(input) == 0 {
		appendText(at, len(d.Text))
	}
	for _, path := range d.AnswerImages {
		input = append(input, binding.ImageInput(path))
	}
	for _, attachment := range d.Attachments {
		input = append(input, binding.AttachmentInput(attachment))
	}
	return input
}
