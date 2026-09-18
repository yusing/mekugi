package mekugi

import (
	"context"
	"fmt"
	"io/fs"
)

type loadedFile struct {
	content string
	mode    fs.FileMode
}

type (
	fileLoader   func(path string) (loadedFile, error)
	pathResolver func(path string) (string, error)
)

type fileState struct {
	originalPath string
	path         string
	original     string
	mode         fs.FileMode
	editor       editor
}

type changeKind int

const (
	changeAdd changeKind = iota
	changeUpdate
	changeDelete
)

type change struct {
	kind         changeKind
	originalPath string
	path         string
	original     string
	content      string
	mode         fs.FileMode
}

type workspace struct {
	paths         map[string]*fileState
	files         []*fileState
	lastPath      string
	reportedEdits []*reportedEdit
	load          fileLoader
}

// evaluate evaluates the program instructions against the workspace.
func (p *program) evaluate(ctx context.Context, resolve pathResolver, load fileLoader) ([]change, string, []TargetAlias, error) {
	w := &workspace{
		paths: make(map[string]*fileState),
		load:  load,
	}
	for _, path := range p.paths {
		if err := ctx.Err(); err != nil {
			return nil, "", nil, err
		}
		resolved, err := resolve(path)
		if err != nil {
			return nil, "", nil, &commandError{
				Reason: reasonPath, Operation: "file", Path: path,
				Category: "path", Message: err.Error(),
			}
		}
		if _, err := w.fileForPath(resolved); err != nil {
			return nil, "", nil, &commandError{
				Reason: reasonOf(err, reasonOther), Operation: "file", Path: path,
				Category: "path", Message: err.Error(),
			}
		}
	}
	var baselineFailures []*commandError
	for commandIndex, command := range p.instructions {
		if err := ctx.Err(); err != nil {
			return nil, "", nil, err
		}
		resolved, err := resolve(command.path)
		if err != nil {
			if failure := w.indentationFailure(); failure != nil {
				return nil, "", nil, failure
			}
			return nil, "", nil, &commandError{
				Target: command.target.variant(), Reason: reasonPath,
				Command: commandIndex + 1, Line: command.line,
				Operation: command.operation, Path: command.path,
				Category: commandCategory(command.operation), Source: command.source,
				Message: err.Error(),
			}
		}
		command.path = resolved

		if err := w.execute(command, commandIndex+1); err != nil {
			if failure := w.indentationFailure(); failure != nil {
				return nil, "", nil, failure
			}

			reason := reasonOf(err, reasonOther)
			failure := &commandError{
				Target: command.target.variant(), Reason: reason,
				Command: commandIndex + 1, Line: command.line,
				Operation: command.operation, Path: w.diagnosticPath(command),
				Category: commandCategory(command.operation), Source: command.source,
				Message: err.Error(), Repair: w.repairContext(command, reason),
			}
			if independentlyDetectableBaselineFailure(reason) {
				baselineFailures = append(baselineFailures, failure)
				continue
			}
			return nil, "", nil, failure
		}
	}
	if len(baselineFailures) != 0 {
		return nil, "", nil, commandFailures(baselineFailures)
	}
	if failure := w.indentationFailure(); failure != nil {
		return nil, "", nil, failure
	}

	if err := ctx.Err(); err != nil {
		return nil, "", nil, err
	}
	if failure := w.renderFinal(ctx); failure != nil {
		return nil, "", nil, failure
	}
	if err := ctx.Err(); err != nil {
		return nil, "", nil, err
	}
	changes := w.changes()

	report, aliases := w.finalStateReport(changes)
	return changes, report, aliases, nil
}

// independentlyDetectableBaselineFailure reports whether a failure can be detected independently.
func independentlyDetectableBaselineFailure(reason failureReason) bool {
	switch reason {
	case reasonRowMissing, reasonRowStale, reasonOccurrenceMissing, reasonTargetOrder:
		return true
	default:
		return false
	}
}

// commandFailures wraps one or more command failures as an error.
func commandFailures(failures []*commandError) error {
	if len(failures) == 1 {
		return failures[0]
	}
	return &commandGroupError{commands: failures}
}

// indentationFailure finds the earliest indentation-only edit that should be rejected.
func (w *workspace) indentationFailure() *commandError {
	var earliest *commandError
	for _, file := range w.files {
		for _, edit := range file.editor.edits {
			if edit.indentation == nil ||
				edit.indentation.candidate.kind != indentationCorrectionExact ||
				indentationPolicy(file.path) != indentationPolicyReject {
				continue
			}
			command := edit.indentation.command
			path := edit.indentation.path
			if path == "" {
				path = file.path
			}
			failure := &commandError{
				Target:          command.target.variant(),
				Reason:          reasonEditConflict,
				Command:         edit.command,
				Line:            command.line,
				Operation:       command.operation,
				Path:            path,
				Category:        commandCategory(command.operation),
				Source:          command.source,
				Message:         edit.indentation.candidate.correction.Error(),
				Repair:          edit.indentation.candidate.correction.diagnostic(),
				CorrectionScope: "field-local",
			}
			if earliest == nil || failure.Command < earliest.Command {
				earliest = failure
			}
		}
	}
	return earliest
}

// diagnosticPath returns the appropriate file path for diagnostic messages.
// diagnosticPath returns the appropriate file path for diagnostic messages.
func (w *workspace) diagnosticPath(command instruction) string {
	if command.path != "" {
		return command.path
	}
	return w.lastPath
}

// commandCategory returns the diagnostic category for an operation.
// commandCategory returns the diagnostic category for an operation.
func commandCategory(operation string) string {
	switch operation {
	case "type", "add", "append":
		return "edit"
	default:
		panic("parsed instruction has no diagnostic category: " + operation)
	}
}

// execute executes a single instruction against the workspace.
func (w *workspace) execute(command instruction, commandIndex int) error {
	origin := editOrigin{
		command: commandIndex, line: command.line,
		operation: command.operation, target: command.target.variant(), targetSpec: command.target,
		multilineValue: command.delimiter != "",
	}
	file, err := w.fileForPath(command.path)
	if err != nil {
		return err
	}
	switch command.operation {
	case "type", "add":
		if command.target.kind == targetNone {
			return withReason(reasonInitialization, fmt.Errorf("%s requires an explicit target", command.operation))
		}
	case "append":
		if command.target.kind != targetNone {
			return withReason(reasonSyntax, fmt.Errorf("append does not accept a target"))
		}
	default:
		return withReason(reasonSyntax, fmt.Errorf("unknown edit operation %q", command.operation))
	}

	if err := file.editor.applyMutation(command.operation, command.target, command.text, origin, command, file.path); err != nil {
		return err
	}
	if edit := file.editor.reportedEdit(origin, command); edit != nil {
		edit.file = file
		w.reportedEdits = append(w.reportedEdits, edit)
	}
	return nil
}

// fileForPath loads one immutable baseline per resolved path.
func (w *workspace) fileForPath(path string) (*fileState, error) {
	if file := w.paths[path]; file != nil {
		w.lastPath = path
		return file, nil
	}
	loaded, err := w.load(path)
	if err != nil {
		return nil, err
	}
	file := &fileState{
		originalPath: path,
		path:         path,
		original:     loaded.content,
		mode:         loaded.mode,
		editor:       editor{baseline: loaded.content},
	}
	w.paths[path] = file
	w.files = append(w.files, file)
	w.lastPath = path
	return file, nil
}

// changes computes the final set of filesystem changes.
func (w *workspace) changes() []change {
	changes := make([]change, 0, len(w.files))
	for _, file := range w.files {
		content := file.editor.content()
		if file.original != content {
			changes = append(changes, change{
				kind:         changeUpdate,
				originalPath: file.originalPath,
				path:         file.path,
				original:     file.original,
				content:      content,
				mode:         file.mode,
			})
		}
	}
	return changes
}
