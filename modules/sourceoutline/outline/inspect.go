package outline

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/yusing/mekugi/libraries/sourceanalysis/structure"

	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/libraries/commandargs"
	"github.com/yusing/mekugi/libraries/outputbudget"

	"github.com/yusing/mekugi/libraries/sourceanalysis/sourcekind"
)

type inspectionData struct {
	Path     string            `json:"path"`
	Kind     string            `json:"kind"`
	Language *string           `json:"language"`
	Size     int64             `json:"size_bytes"`
	Lines    *int              `json:"line_count"`
	Complete bool              `json:"parse_complete"`
	Outline  []structure.Entry `json:"outline"`
}
type inspectFailure struct{ code, message string }

func (e *inspectFailure) Error() string { return e.message }
func inspectCode(err error) string {
	if e, ok := errors.AsType[*inspectFailure](err); ok {
		return e.code
	}
	if errors.Is(err, os.ErrNotExist) {
		return "not_found"
	}
	return "read"
}
func inspectClass(code string) string {
	switch code {
	case "usage":
		return "invalid_arguments"
	case "not_found", "output_limit":
		return code
	}
	return "reader_error"
}
func inspectDisplay(s string) string {
	if !utf8.ValidString(s) {
		value, _ := structure.String(s).MarshalJSON()
		return string(value)
	}
	for _, r := range s {
		if r < 32 || r == 127 {
			return encodeJSON(s)
		}
	}
	return s
}
func inspectEnvelope(data inspectionData, reason string, count int) string {
	var truncation any
	if reason != "" {
		truncation = map[string]any{"reason": reason, "after_entries": count}
		data.Outline = data.Outline[:count]
	}
	return encodeJSON(struct {
		OK         bool           `json:"ok"`
		Data       inspectionData `json:"data"`
		Truncated  bool           `json:"truncated"`
		Truncation any            `json:"truncation"`
	}{true, data, reason != "", truncation}) + "\n"
}
func inspectNativePath(ctx context.Context, path string) (inspectionData, error) {
	if path == "" || strings.ContainsRune(path, 0) {
		return inspectionData{}, &inspectFailure{"usage", "inspect_file expects exactly one usable path"}
	}
	path = filepath.Clean(path)
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return inspectionData{}, &inspectFailure{"not_found", "path does not exist"}
		}
		return inspectionData{}, err
	}
	if !info.Mode().IsRegular() {
		return inspectionData{}, &inspectFailure{"not_regular", "path is not a regular file"}
	}
	data := inspectionData{Path: filepath.ToSlash(path), Kind: "none", Size: info.Size(), Complete: true, Outline: []structure.Entry{}}
	format, ok := sourcekind.Classify(path)
	if !ok || !format.Outline {
		return data, nil
	}
	bytes, err := os.ReadFile(path)
	if err != nil {
		return data, err
	}
	return inspectSource(ctx, data, bytes, format)
}

// InspectSource uses the same structural renderer as inspect_file on a caller's
// already-opened snapshot, without reopening a path or executing a frontend.
func InspectSource(ctx context.Context, path string, bytes []byte) (string, error) {
	data := inspectionData{Path: filepath.ToSlash(path), Size: int64(len(bytes)), Complete: true}
	format, ok := sourcekind.Classify(path)
	if !ok || !format.Outline {
		return "(no outline)\n", nil
	}
	data, err := inspectSource(ctx, data, bytes, format)
	if err != nil {
		return "", err
	}
	return strings.Join(compactNativeOutline(data), ""), nil
}

func inspectSource(ctx context.Context, data inspectionData, bytes []byte, format sourcekind.Format) (inspectionData, error) {
	if !utf8.Valid(bytes) {
		return data, &inspectFailure{"not_utf8", "supported file is not valid UTF-8"}
	}
	parsed, err := structure.Parse(ctx, string(bytes), format)
	if err != nil {
		return data, &inspectFailure{"parse", err.Error()}
	}
	data.Kind = format.Kind
	if format.Language != "" {
		data.Language = new(format.Language)
	}
	data.Size = int64(len(bytes))
	data.Lines = new(parsed.Lines.Len())
	for _, entry := range parsed.Entries {
		data.Outline = append(data.Outline, entry)
		if entry.Kind == "parse_error" {
			data.Complete = false
		}
	}
	return data, nil
}
func compactNativeOutline(data inspectionData) []string {
	importsStart, importsEnd := 0, 0
	for _, e := range data.Outline {
		if e.Kind == "import" {
			start, end := e.Line, e.EndLine
			if importsStart == 0 || start < importsStart {
				importsStart = start
			}
			importsEnd = max(importsEnd, end)
		}
	}
	var rows []string
	for _, e := range data.Outline {
		kind := e.Kind
		if kind == "import" {
			if importsStart > 0 {
				rows = append(rows, fmt.Sprintf("%d-%d import\n", importsStart, importsEnd))
				importsStart = 0
			}
			continue
		}
		name := e.Name
		if kind == "method" {
			name = e.Receiver + "." + name
		}
		if kind == "json" {
			name = e.Pointer
			if name == "" {
				name = "/"
			}
			name += " " + e.ValueType
		}
		rows = append(rows, fmt.Sprintf("%d-%d %s %s\n", e.Line, e.EndLine, kind, inspectDisplay(name)))
	}
	if len(rows) == 0 {
		return []string{"(no outline)\n"}
	}
	return rows
}

// Source: plugins/inspect_file.ts:139:356@[543de4f3] success/createInspectFileTool
func Execute(ctx context.Context, args []string, _ *os.File) (command.Output, error) {
	if err := ctx.Err(); err != nil {
		return command.Output{}, err
	}
	asJSON := false
	for _, a := range args {
		if a == "--" {
			break
		}
		if a == "--json" {
			asJSON = true
		}
	}
	failure := func(path *string, err error) command.Output {
		code := inspectCode(err)
		stderr := "inspect_file: "
		if path != nil {
			stderr += inspectDisplay(*path) + ": "
		}
		stderr += err.Error() + "\n"
		if asJSON {
			stderr = encodeJSON(map[string]any{"ok": false, "path": path, "error": map[string]string{"code": code, "message": err.Error()}}) + "\n"
		}
		return command.Output{Stderr: stderr, ExitCode: 1, FailureClass: inspectClass(code)}
	}
	options, rest, err := commandargs.ParseReaderOptions(args, false, 4000, "")
	if err != nil {
		return failure(nil, &inspectFailure{"usage", err.Error()}), nil
	}
	var paths []string
	seen, ended := false, false
	for _, a := range rest {
		if !ended && a == "--" {
			ended = true
			continue
		}
		if !ended && a == "--json" {
			if seen {
				return failure(nil, &inspectFailure{"usage", "--json cannot repeat"}), nil
			}
			seen = true
			continue
		}
		if !ended && strings.HasPrefix(a, "--preview-bytes") {
			return failure(nil, &inspectFailure{"usage", "inspect_file does not accept source preview flags"}), nil
		}
		paths = append(paths, a)
	}
	if len(paths) == 0 {
		return failure(nil, &inspectFailure{"usage", "inspect_file expects PATH [PATH ...]"}), nil
	}
	counter := outputbudget.Budget{}
	fits := func(s string) (bool, error) {
		if len(s) > 65536 {
			return false, nil
		}
		return counter.Fits(s, options.Budget)
	}
	result := command.Output{}
	var output, omitted strings.Builder
	incomplete, unavailable := false, false
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return command.Output{}, err
		}
		data, e := inspectNativePath(ctx, path)
		if e != nil {
			f := failure(&path, e)
			result.Stderr += f.Stderr
			result.ExitCode = 1
			if result.FailureClass == "" {
				result.FailureClass = f.FailureClass
			}
			continue
		}
		if asJSON && len(paths) == 1 {
			complete := inspectEnvelope(data, "", 0)
			fit, e := fits(complete)
			if e != nil {
				return command.Output{}, e
			}
			if fit {
				result.Stdout = complete
				return result, nil
			}
			reason := "output_tokens"
			if len(complete) > 65536 {
				reason = "output_bytes"
			}
			low, high, selected := 0, len(data.Outline)-1, -1
			for low <= high {
				mid := (low + high) / 2
				fit, e := fits(inspectEnvelope(data, reason, mid))
				if e != nil {
					return command.Output{}, e
				}
				if fit {
					selected = mid
					low = mid + 1
				} else {
					high = mid - 1
				}
			}
			if selected < 0 {
				return failure(&path, &inspectFailure{"output_limit", "minimum success result exceeds the output budget; increase --max-tokens or shorten the path"}), nil
			}
			result.Stdout = inspectEnvelope(data, reason, selected)
			result.ExitCode = 1
			result.FailureClass = "output_limit"
			retained := encodeJSON(data.Outline[selected:])
			if len(retained) > retainedBytes {
				result.Stderr = "inspect_file: recovery unavailable: omitted outline exceeds the 16 MiB recovery bound; use bounded mcat reads\n"
			} else {
				result.OmittedOutput = &command.OmittedOutput{Stdout: retained, StdoutKind: "json"}
			}
			return result, nil
		}
		rows := compactNativeOutline(data)
		if asJSON {
			rows = []string{inspectEnvelope(data, "", 0)}
		} else if len(paths) > 1 {
			rows = append([]string{"--- " + inspectDisplay(data.Path) + " ---\n"}, rows...)
		}
		for _, row := range rows {
			if !incomplete && output.Len()+len(row) <= 65536 {
				output.WriteString(row)
			} else {
				incomplete = true
				if !unavailable {
					if omitted.Len()+len(row) > retainedBytes {
						unavailable = true
						omitted.Reset()
					} else {
						omitted.WriteString(row)
					}
				}
			}
		}
	}
	result.Stdout, err = counter.SelectRows(output.String(), options.Budget, false)
	if err != nil {
		return command.Output{}, err
	}
	if len(result.Stdout) < output.Len() {
		incomplete = true
		if !unavailable {
			if output.Len()-len(result.Stdout)+omitted.Len() > retainedBytes {
				unavailable = true
			} else {
				remaining := output.String()[len(result.Stdout):] + omitted.String()
				omitted.Reset()
				omitted.WriteString(remaining)
			}
		}
	}
	if incomplete {
		result.ExitCode = 1
		result.FailureClass = "output_limit"
		if unavailable {
			result.Stderr += "inspect_file: recovery unavailable: omitted outline exceeds the 16 MiB recovery bound; use bounded mcat reads\n"
		} else {
			kind := "rows"
			if asJSON {
				kind = ""
			}
			result.OmittedOutput = &command.OmittedOutput{Stdout: omitted.String(), StdoutKind: kind}
		}
	}
	return result, nil
}

const retainedBytes = 16 << 20

func encodeJSON(v any) string {
	data, err := json.Marshal(v, json.Deterministic(true), jsontext.PreserveRawStrings(true), jsontext.AllowInvalidUTF8(true))
	if err != nil {
		panic(err)
	}
	return string(data)
}
