package mekugi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/yusing/mekugi/internal/hpatchsyntax"
)

// PreviewForHostAt projects unfinished edit text into a disposable in-memory
// workspace. It never formats, validates languages, runs hooks, or applies files.
// Paths and targets use the complete edit's owners; this is not edit evidence.
func PreviewForHostAt(ctx context.Context, directory string, edits []FileEdit) ([]ReviewFile, error) {
	const limit = 256 << 10
	totalInput := 0
	for _, edit := range edits {
		totalInput += len(edit.Script)
	}
	if totalInput > limit {
		return nil, fmt.Errorf("streaming preview exceeds %d bytes", limit)
	}
	filesystem, err := validateHostDirectory(ctx, directory)
	if err != nil {
		return nil, err
	}
	remaining := limit
	w := &workspace{
		paths: make(map[string]*fileState),
		load: func(path string) (loadedFile, error) {
			if err := ctx.Err(); err != nil {
				return loadedFile{}, err
			}
			info, err := filesystem.stat(path)
			if err != nil {
				return loadedFile{}, err
			}
			if !info.Mode().IsRegular() || info.Size() > int64(remaining) {
				return loadedFile{}, errors.New("streaming preview requires bounded regular files")
			}
			file, err := filesystem.open(path)
			if err != nil {
				return loadedFile{}, err
			}
			defer file.Close()
			data, err := io.ReadAll(io.LimitReader(file, int64(remaining)+1))
			if err != nil || len(data) > remaining {
				return loadedFile{}, errors.New("streaming preview source exceeds capacity")
			}
			if !utf8.Valid(data) {
				return loadedFile{}, errors.New("streaming preview source is not UTF-8")
			}
			remaining -= len(data)
			return loadedFile{content: string(data), mode: info.Mode()}, nil
		},
	}
	mutations := 0
	commandIndex := 0
	for _, edit := range edits {
		input := edit.Script
		previewPath, err := filesystem.resolvePath(edit.Path)
		if err != nil {
			return nil, err
		}
		if _, err := w.fileForPath(previewPath); err != nil {
			return nil, err
		}
		lines := hpatchsyntax.SplitPhysicalLines(input)
		for index := 0; index < len(lines); {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			start := index
			line := lines[start].Text
			commandIndex++
			if strings.TrimSpace(line) == "" {
				index++
				continue
			}
			frame, frameErr := hpatchsyntax.FrameCommand(lines, start, line)
			index = frame.Next
			var command instruction
			if frame.Marker != "" {
				if lines[start].Terminator == "" {
					break
				}
				if frameErr != nil {
					delimiter := frame.Delimiter
					tail := input
					last := lines[len(lines)-1].Text
					// A delimiter split over deltas is framing, not source.
					delimiterPrefix := last
					if frame.StripTabs {
						delimiterPrefix = strings.TrimLeft(last, "\t")
					}
					if delimiterPrefix != "" && strings.HasPrefix(delimiter, delimiterPrefix) {
						tail = strings.TrimSuffix(tail, last)
					}
					addedNewline := !strings.HasSuffix(tail, "\n")
					if addedNewline {
						tail += "\n"
					}
					projected := hpatchsyntax.SplitPhysicalLines(tail + delimiter + "\n")
					frame, frameErr = hpatchsyntax.FrameCommand(projected, start, line)
					if addedNewline {
						frame.Body = strings.TrimSuffix(frame.Body, "\n")
					}
					index = len(lines)
				}
				if frameErr != nil {
					break
				}
				command, err = parseInstructionWithValue(start+1, strings.TrimSuffix(line, frame.Marker), frame.Body, true)
				command.delimiter = frame.Marker
			} else {
				if frameErr != nil || lines[start].Terminator == "" {
					if !strings.HasPrefix(line, "type ") && !strings.HasPrefix(line, "add ") && !strings.HasPrefix(line, "append ") {
						break
					}
					command, err = previewInline(start+1, line)
				} else {
					command, err = parseInstruction(start+1, line)
				}
			}
			if err != nil {
				break // Incomplete syntax is not a rejected edit.
			}
			command.source, command.lineTerminator = line, lines[start].Terminator
			command.path = previewPath
			// Target expansion enters the shared editor's conflict checks. Bound
			// speculative work before those checks, not merely the result bytes.
			if command.operation == "type" || command.operation == "add" || command.operation == "append" {
				cost := max(1, command.target.count)
				if cost > 1024-mutations {
					return nil, errors.New("streaming preview exceeds 1,024 target mutations")
				}
				mutations += cost
			}
			if err := w.execute(command, commandIndex); err != nil {
				return nil, err
			}
			total := 0
			for _, file := range w.files {
				if !file.editor.contentFits(limit) {
					return nil, errors.New("streaming preview result exceeds capacity")
				}
				total += len(file.editor.content())
			}
			if total > limit {
				return nil, errors.New("streaming preview result exceeds capacity")
			}
		}
	}
	files := reviewFiles(w.changes())
	for i := range files {
		if files[i].BeforePath != "" {
			files[i].BeforePath = filesystem.hostPath(files[i].BeforePath)
		}
		if files[i].AfterPath != "" {
			files[i].AfterPath = filesystem.hostPath(files[i].AfterPath)
		}
	}
	return files, nil
}

// Complete only the final quoted value for display. Incomplete escapes and UTF-8
// wait for the next delta; target interpretation still belongs to the parser.
func previewInline(lineNumber int, line string) (instruction, error) {
	if command, err := parseInstruction(lineNumber, line); err == nil {
		return command, nil
	}
	for trim := 0; trim <= 12 && trim < len(line); trim++ {
		prefix := line[:len(line)-trim]
		if !utf8.ValidString(prefix) {
			continue
		}
		if command, err := parseInstruction(lineNumber, prefix+`"`); err == nil {
			return command, nil
		}
	}
	return instruction{}, errors.New("incomplete streaming value")
}
