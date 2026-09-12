package mekugi

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
)

type loadedFile struct {
	content string
	mode    fs.FileMode
}

type (
	fileLoader   func(path string) (loadedFile, error)
	pathProbe    func(path string) (fs.FileMode, bool, error)
	pathResolver func(path string) (string, error)
)

type fileState struct {
	originalPath   string
	path           string
	original       string
	mode           fs.FileMode
	created        bool
	deleted        bool
	mutationOrigin editOrigin
	editor         editor
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
	paths map[string]*fileState
	// Vacated paths remain unavailable for both selection and destinations.
	reserved      map[string]bool
	files         []*fileState
	active        *fileState
	initializable *fileState
	reportedEdits []*reportedEdit
	load          fileLoader
	exists        pathProbe
}

// evaluate evaluates the program instructions against the workspace.
func (p *program) evaluate(ctx context.Context, resolve pathResolver, load fileLoader, exists pathProbe) ([]change, string, []TargetAlias, error) {
	w := &workspace{
		paths:    make(map[string]*fileState),
		reserved: make(map[string]bool),
		load:     load,
		exists:   exists,
	}
	var baselineFailures []*commandError
	for commandIndex, command := range p.instructions {
		if err := ctx.Err(); err != nil {
			return nil, "", nil, err
		}
		diagnosticPath := command.path
		if command.path != "" {
			resolved, err := resolve(command.path)
			if err != nil {
				if failure := w.indentationFailure(); failure != nil {
					return nil, "", nil, failure
				}

				reason := reasonPath
				return nil, "", nil, &commandError{Target: command.target.variant(), Reason: reason, Command: commandIndex + 1, Line: command.line, Operation: command.operation, Path: diagnosticPath, Category: commandCategory(command.operation), Source: command.source, Message: err.Error()}
			}
			command.path = resolved
		}

		if err := w.execute(command, commandIndex+1); err != nil {
			if failure := w.indentationFailure(); failure != nil {
				return nil, "", nil, failure
			}

			reason := reasonOf(err, reasonOther)
			failure := &commandError{Target: command.target.variant(), Reason: reason, Command: commandIndex + 1, Line: command.line, Operation: command.operation, Path: w.diagnosticPath(command), Category: commandCategory(command.operation), Source: command.source, Message: err.Error(), Repair: w.repairContext(command, reason)}
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
func (w *workspace) diagnosticPath(command instruction) string {
	if command.path != "" {
		return command.path
	}
	if w.active != nil {
		return w.active.path
	}
	return ""
}

// commandCategory returns the diagnostic category for an operation.
func commandCategory(operation string) string {
	switch operation {
	case "in", "new", "mv", "rm":
		return "file"
	case "type", "add":
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
	initializing := command.operation == "type" && command.target.kind == targetNone && w.initializable == w.active
	if !initializing {
		w.initializable = nil
	}
	switch command.operation {
	case "in":
		return w.selectFile(command.path)
	case "new":
		return w.newFile(command.path, origin)
	case "mv":
		return w.moveFile(command.path, origin)
	case "rm":
		return w.removeFile()
	}
	if w.active == nil {
		return withReason(reasonActiveFile, fmt.Errorf("%s requires an active file", command.operation))
	}

	file := w.active
	if command.target.kind == targetNone {
		if !initializing || !file.created {
			return withReason(reasonInitialization, fmt.Errorf("bare type VALUE only initializes the immediately preceding new; editing an existing file requires a line, range, or text target"))
		}
		file.editor.initialize(command.text, origin)
		w.initializable = nil
	} else {
		if file.created {
			return withReason(reasonInitialization, fmt.Errorf("new file content is not targetable before a successful invocation and hcat"))
		}
		err := file.editor.applyMutation(command.operation, command.target, command.text, origin, command, file.path)
		if err != nil {
			return err
		}
	}
	if edit := file.editor.reportedEdit(origin, command); edit != nil {
		edit.file = file
		w.reportedEdits = append(w.reportedEdits, edit)
	}
	return nil
}

// selectFile makes an existing file the active file, loading it if necessary.
func (w *workspace) selectFile(path string) error {
	if file := w.paths[path]; file != nil {
		w.active = file
		return nil
	}
	if w.reserved[path] {
		return withReason(reasonFileMissing, fmt.Errorf("%s does not exist in the pending workspace", path))
	}
	loaded, err := w.load(path)
	if err != nil {
		return err
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
	w.active = file
	return nil
}

// newFile creates a new file at the given path.
func (w *workspace) newFile(path string, origin editOrigin) error {
	if err := w.validateFreeDestination(path); err != nil {
		return err
	}
	file := &fileState{path: path, created: true, mutationOrigin: origin}
	w.paths[path] = file
	w.files = append(w.files, file)
	w.active = file
	w.initializable = file
	return nil
}

// moveFile moves the active file to a new path.
func (w *workspace) moveFile(path string, origin editOrigin) error {
	if w.active == nil {
		return withReason(reasonActiveFile, fmt.Errorf("mv requires an active file"))
	}
	if err := w.validateFreeDestination(path); err != nil {
		return err
	}
	oldPath := w.active.path
	delete(w.paths, oldPath)
	w.reserved[oldPath] = true
	w.active.path = path
	w.active.mutationOrigin = origin
	w.paths[path] = w.active
	return nil
}

// removeFile deletes the active file from the workspace.
func (w *workspace) removeFile() error {
	if w.active == nil {
		return withReason(reasonActiveFile, fmt.Errorf("rm requires an active file"))
	}
	file := w.active
	if !file.created {
		if edit, ok := file.editor.firstEdit(); ok {
			return withReason(reasonEditConflict, fmt.Errorf(
				"cannot remove a baseline file after content edit from command %d (source line %d, operation %q)",
				edit.command,
				edit.line,
				edit.operation,
			))
		}
	}
	removedPath := file.path
	delete(w.paths, removedPath)
	w.reserved[removedPath] = true
	file.deleted = true
	retained := w.reportedEdits[:0]
	for _, edit := range w.reportedEdits {
		if edit.file != file {
			retained = append(retained, edit)
		}
	}
	w.reportedEdits = retained
	w.active = nil
	w.initializable = nil
	return nil
}

// pathOccupied checks if a path is occupied in the workspace or filesystem.
func (w *workspace) pathOccupied(path string) (bool, error) {
	if w.paths[path] != nil || w.reserved[path] {
		return true, nil
	}
	_, occupied, err := w.exists(path)
	if err != nil {
		return false, fmt.Errorf("checking destination %s: %w", path, err)
	}
	return occupied, nil
}

// validateDestinationParent checks that the parent directory exists.
func (w *workspace) validateDestinationParent(path string) error {
	parent := filepath.Dir(path)
	mode, exists, err := w.exists(parent)
	if err != nil {
		return fmt.Errorf("checking parent directory %s: %w", parent, err)
	}
	if !exists || !mode.IsDir() {
		return withReason(reasonPath, fmt.Errorf("parent directory %s does not exist", parent))
	}
	return nil
}

// validateFreeDestination checks that a destination path is available.
func (w *workspace) validateFreeDestination(path string) error {
	if err := w.validateDestinationParent(path); err != nil {
		return err
	}
	occupied, err := w.pathOccupied(path)
	if err != nil {
		return err
	}
	if occupied {
		return withReason(reasonFileConflict, fmt.Errorf("destination %s already exists", path))
	}
	return nil
}

// changes computes the final set of filesystem changes.
func (w *workspace) changes() []change {
	changes := make([]change, 0, len(w.files))
	for _, file := range w.files {
		content := file.editor.content()
		switch {
		case file.created && file.deleted:
			continue
		case file.created:
			changes = append(changes, change{kind: changeAdd, path: file.path, content: content, mode: 0o644})
		case file.deleted:
			changes = append(changes, change{
				kind:         changeDelete,
				originalPath: file.originalPath,
				original:     file.original,
				mode:         file.mode,
			})
		case file.originalPath != file.path || file.original != content:
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
