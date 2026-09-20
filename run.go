package mekugi

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/yusing/mekugi/internal/verifiedrow"
)

// Workspace is the filesystem authority for one workspace edit. Root should
// be opened from its canonical absolute path; absolute script paths are matched
// against that name. CWD is root-relative and defaults to ".".
// Callers coordinate writers to overlapping files and lifecycle paths, from the
// reads used to author an edit through application and any rollback. Sharing a
// Root does not serialize operations or provide a filesystem snapshot.
type Workspace struct {
	Root *os.Root
	CWD  string
}

// EditText applies target-bearing HPATCH mutations to an in-memory immutable
// baseline. It performs no filesystem access, language validation, formatting,
// indentation correction, or whitespace cleanup.
func EditText(ctx context.Context, baseline, script string) (string, error) {
	return editText(ctx, baseline, script, -1)
}

// EditTextBounded is EditText with a byte limit on the baseline and the planned
// result after each command. It checks sizes before rendering expanded content.
// maxBytes must be nonnegative; zero permits only empty content.
func EditTextBounded(ctx context.Context, baseline, script string, maxBytes int) (string, error) {
	if maxBytes < 0 {
		return "", fmt.Errorf("text edit byte limit must be nonnegative")
	}
	return editText(ctx, baseline, script, maxBytes)
}

func editText(ctx context.Context, baseline, script string, maxBytes int) (string, error) {
	if ctx == nil {
		return "", fmt.Errorf("context is nil")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if maxBytes >= 0 && len(baseline) > maxBytes {
		return "", fmt.Errorf("text edit baseline exceeds %d bytes", maxBytes)
	}
	program, err := parse(script)
	if err != nil {
		return "", err
	}
	target := &editor{baseline: baseline}
	for index, command := range program.instructions {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if command.target.kind == targetNone ||
			(command.operation != "type" && command.operation != "add") {
			return "", textEditCommandError(
				command,
				index+1,
				reasonSyntax,
				"text edit accepts only target-bearing type or add",
			)
		}
		origin := editOrigin{
			command:        index + 1,
			line:           command.line,
			operation:      command.operation,
			target:         command.target.variant(),
			targetSpec:     command.target,
			multilineValue: command.delimiter != "",
		}
		if err := target.applyMutation(command.operation, command.target, command.text, origin, command, ""); err != nil {
			return "", textEditCommandError(command, index+1, reasonOf(err, reasonOther), err.Error())
		}
		if maxBytes >= 0 && !target.contentFits(maxBytes) {
			return "", fmt.Errorf("text edit command %d result exceeds %d bytes", index+1, maxBytes)
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return target.content(), nil
}

// TargetIdentity is the comparable semantic identity of one HPATCH target.
// Its representation is intentionally opaque so target syntax remains owned by
// the root parser.
type TargetIdentity struct {
	target targetSpec
}

// ParseTargetIdentity parses the target prefix in source and returns the
// unconsumed source. mutationValueFollows disambiguates a quoted value after a
// row from an anchored literal target. Single-row ranges share a line target's
// identity, and omitted occurrence counts share an explicit count of one.
func ParseTargetIdentity(source string, mutationValueFollows bool) (TargetIdentity, string, error) {
	target, trailing, err := parseTarget(1, source, mutationValueFollows)
	if err != nil {
		return TargetIdentity{}, "", err
	}
	if target.kind == targetEOF {
		return TargetIdentity{}, "", fmt.Errorf("EOF is an add destination, not a target")
	}
	if target.kind == targetRange && target.start == target.end {
		target.kind = targetLine
		target.end = rowReference{}
	}
	return TargetIdentity{target: target}, trailing, nil
}

// ParseInlineMutation decodes a target-bearing type/add command using the edit
// parser. Target source is normalized to put any count before the value, so
// recovery can replace a value without dropping its occurrence selection.
func ParseInlineMutation(source string) (targetSource, value string, identity TargetIdentity, err error) {
	command, err := parseInstruction(1, source)
	if err != nil {
		return "", "", TargetIdentity{}, err
	}
	if command.operation != "type" && command.operation != "add" {
		return "", "", TargetIdentity{}, fmt.Errorf("expected a target-bearing mutation")
	}
	_, operands, _ := strings.Cut(source[:command.valueStart], " ")
	targetSource = strings.TrimSpace(operands)
	if (command.target.kind == targetLiteral || command.target.kind == targetText) &&
		strings.HasSuffix(targetSource, `"`) && command.target.count > 1 {
		targetSource += " " + strconv.Itoa(command.target.count)
	}
	identity, _, err = ParseTargetIdentity(targetSource, false)
	return targetSource, command.text, identity, err
}

// textEditCommandError creates a command error for text editing failures.
func textEditCommandError(command instruction, index int, reason failureReason, message string) *commandError {
	return &commandError{
		Target:    command.target.variant(),
		Reason:    reason,
		Command:   index,
		Line:      command.line,
		Operation: command.operation,
		Category:  "edit",
		Source:    command.source,
		Message:   message,
	}
}

// TextLineCount returns the number of targetable logical rows in text.
func TextLineCount(text string) int {
	return verifiedrow.Count(text)
}

// TextReferences renders current LINE:HASH references for valid requested rows.
// Repeated and out-of-range row numbers are omitted.
func TextReferences(text string, rows ...int) string {
	lines := logicalLines(text)
	seen := make(map[int]struct{}, len(rows))
	var output strings.Builder
	for _, number := range rows {
		if number < 1 || number > len(lines) {
			continue
		}
		if _, exists := seen[number]; exists {
			continue
		}
		seen[number] = struct{}{}
		content := lineContent(text, lines[number-1])
		writeHashLine(&output, number, content, previewTextLimit(content, repairPreviewLimit))
	}
	return output.String()
}

type FileEdit struct {
	Path   string
	Script string
}

func joinedFileEditScripts(edits []FileEdit) string {
	var output strings.Builder
	for index, edit := range edits {
		if index != 0 {
			output.WriteByte('\n')
		}
		output.WriteString(edit.Script)
	}
	return output.String()
}

// TargetAlias maps a target from one successful script to the rendered region
// that replaced it. Hosts may retain aliases only after the translated patch
// was applied successfully.
type TargetAlias struct {
	Path   string
	Before string
	After  string
}

// Apply evaluates the complete set of file edits before staging and applying
// their changes. Each FileEdit names the existing file whose immutable baseline
// its pathless script targets.
func Apply(ctx context.Context, workspace Workspace, edits []FileEdit) error {
	changes, filesystem, _, _, err := evaluateScript(ctx, workspace, edits)
	if err != nil {
		return err
	}
	if len(changes) == 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return commitChanges(changes, rootFileOperations{root: filesystem.root})
}

// HostRejection is the non-sensitive, structured identity of one rejected
// command. It intentionally excludes source text, diagnostics, and repair
// context so hosts can retain it as telemetry without retaining edit content.
type HostRejection struct {
	Command             int    `json:"command"`
	SourceLine          int    `json:"source_line"`
	Operation           string `json:"operation"`
	Target              string `json:"target,omitempty"`
	TargetAliasRelation string `json:"target_alias_relation,omitempty"`
	Reason              string `json:"reason"`
	Path                string `json:"path,omitempty"`
	GeneratedLine       int    `json:"generated_line,omitempty"`
	GeneratedColumn     int    `json:"generated_column,omitempty"`
	ValueLine           int    `json:"value_line,omitempty"`
}

// HostOutcome identifies the furthest lifecycle stage reached by one host request.
type HostOutcome struct {
	Stage  string `json:"stage"`
	Status string `json:"status"`
}

// HostChange summarizes the requested workspace effect.
type HostChange struct {
	Files            int  `json:"files"`
	AlreadySatisfied bool `json:"already_satisfied"`
	Applied          bool `json:"applied"`
}

// HostFailure is actionable host-facing failure context. Unlike HostRejection,
// it may contain bounded repair text and is not suitable for durable telemetry.
type HostFailure struct {
	Command    int    `json:"command,omitempty"`
	Path       string `json:"path,omitempty"`
	Reason     string `json:"reason"`
	Scope      string `json:"scope"`
	Suggestion string `json:"suggestion,omitempty"`
}

// HostPatchSummary describes a translated patch without duplicating its content.
type HostPatchSummary struct {
	Files int `json:"files"`
	Bytes int `json:"bytes"`
}

// HostTranslation contains the complete result needed by an in-process host.
// Diagnostic contains a rejection diagnostic or non-fatal hook warnings.
type HostTranslation struct {
	ReviewFiles   []ReviewFile
	Patch         []byte
	Report        string
	TargetAliases []TargetAlias
	Diagnostic    string
	Outcome       HostOutcome
	Change        HostChange
	Attempt       AttemptMetadata
	Failures      []HostFailure
	PatchSummary  HostPatchSummary
	Rejections    []HostRejection
}

// RenderFileWritePatch renders a literal, truncating text write for a host's
// apply_patch tool. It does not read, validate source code, format, or apply the
// file. Add File deliberately also overwrites an existing file in that tool,
// so commands executed before the write cannot invalidate a sampled baseline.
// Only empty or LF-terminated text is representable without changing its bytes.
func RenderFileWritePatch(path, content string) (string, error) {
	return renderFileWritePatch(path, content)
}

// TranslateForHostAt evaluates a set of file edits relative to directory
// without imposing filesystem confinement. The host executor remains
// responsible for authorizing and applying the translated patch.
func TranslateForHostAt(ctx context.Context, directory string, edits []FileEdit, dataDirectory string) (HostTranslation, error) {
	if ctx == nil {
		return HostTranslation{}, fmt.Errorf("context is nil")
	}
	changes, _, report, aliases, err := evaluateScriptAt(ctx, directory, edits)
	result := hostTranslationResult(changes, report, aliases, err == nil)
	failureStage := ""
	if err != nil {
		failureStage = "evaluated"
	} else if err = translateHostResult(ctx, changes, &result); err != nil {
		failureStage = "translated"
	}
	return finishHostChange(ctx, dataDirectory, joinedFileEditScripts(edits), result, failureStage, err, false)
}

// ApplyForHost applies a set of file edits with the same caller-coordination
// and commit guarantees as Apply, while returning host diagnostics.
func ApplyForHost(ctx context.Context, workspace Workspace, edits []FileEdit, dataDirectory string) (HostTranslation, error) {
	if ctx == nil {
		return HostTranslation{}, fmt.Errorf("context is nil")
	}
	changes, filesystem, report, aliases, err := evaluateScript(ctx, workspace, edits)
	result := hostTranslationResult(changes, report, aliases, err == nil)
	failureStage := ""
	if err != nil {
		failureStage = "evaluated"
	} else if err = ctx.Err(); err == nil && len(changes) != 0 {
		if err = commitChanges(changes, rootFileOperations{root: filesystem.root}); err != nil {
			err = fmt.Errorf("changing %s: %w", describePaths(changes), err)
			failureStage = "applied"
		}
	}
	return finishHostChange(ctx, dataDirectory, joinedFileEditScripts(edits), result, failureStage, err, true)
}

// ApplyForHostAt evaluates and applies a set of file edits relative to
// directory using the host process's filesystem authority. Like ApplyForHost,
// it evaluates the entire edit set before committing.
func ApplyForHostAt(ctx context.Context, directory string, edits []FileEdit, dataDirectory string) (HostTranslation, error) {
	if ctx == nil {
		return HostTranslation{}, fmt.Errorf("context is nil")
	}
	changes, filesystem, report, aliases, err := evaluateScriptAt(ctx, directory, edits)
	result := hostTranslationResult(changes, report, aliases, err == nil)
	failureStage := ""
	if err != nil {
		failureStage = "evaluated"
	} else if err = ctx.Err(); err == nil {
		if observer, _ := ctx.Value(preWriteObserverKey{}).(func([]ReviewFile)); observer != nil {
			observer(slices.Clone(result.ReviewFiles))
		}
		if err = ctx.Err(); err == nil && len(changes) != 0 {
			if err = commitChanges(changes, hostFileOperations{filesystem: filesystem}); err != nil {
				err = fmt.Errorf("changing %s: %w", describePaths(changes), err)
				failureStage = "applied"
			}
		}
	}
	return finishHostChange(ctx, dataDirectory, joinedFileEditScripts(edits), result, failureStage, err, true)
}

// ApplyForHostRoot evaluates and applies a set of file edits within root.
func ApplyForHostRoot(ctx context.Context, root *os.Root, edits []FileEdit, dataDirectory string) (HostTranslation, error) {
	return ApplyForHost(ctx, Workspace{Root: root}, edits, dataDirectory)
}

// finishHostChange completes a host translation with outcome metadata and hooks.
func finishHostChange(ctx context.Context, dataDirectory, script string, result HostTranslation, failureStage string, err error, applied bool) (HostTranslation, error) {
	// Evaluation prepares the success projection before external effects, but
	// no failing return may publish it. Keep lifecycle/effect metadata separate:
	// late cancellation can still truthfully report that application succeeded.
	report, aliases := result.Report, result.TargetAliases
	patch, summary, review := result.Patch, result.PatchSummary, result.ReviewFiles
	result.ReviewFiles = nil
	result.Report, result.TargetAliases = "", nil
	result.Patch, result.PatchSummary = nil, HostPatchSummary{}
	result.Attempt, _ = attemptMetadataFromContext(ctx)
	if err != nil {
		result.Rejections = hostRejectionsOf(err)
		result.Failures = hostFailuresOf(err, failureStage)
		status := "failed"
		if failureStage == "evaluated" {
			status = "rejected"
		}
		result.Outcome = HostOutcome{Stage: failureStage, Status: status}

		if ctx.Err() == nil {
			result.Diagnostic = evaluationDiagnostic(ctx, err, dataDirectory)
			if contextErr := ctx.Err(); contextErr != nil {
				result.Diagnostic = ""
				return result, contextErr
			}
			for _, hookErr := range runOutcomeHooks(ctx, dataDirectory, failureStage, status, script, nil, errorHooksTimeout) {
				warning := warningDiagnostic(hookErr.Error())
				if !strings.Contains(result.Diagnostic, warning) {
					result.Diagnostic += warning
				}
			}
		}
		if contextErr := ctx.Err(); contextErr != nil {
			result.Diagnostic = ""
			return result, contextErr
		}
		return result, err
	}
	stage, status := "translated", "succeeded"
	if result.Change.AlreadySatisfied {
		stage, status = "evaluated", "already-satisfied"
	} else if applied {
		stage, status = "applied", "succeeded"
		result.Change.Applied = true
	}
	result.Outcome = HostOutcome{Stage: stage, Status: status}
	for _, hookErr := range runOutcomeHooks(ctx, dataDirectory, stage, status, script, patch, errorHooksTimeout) {
		result.Diagnostic += warningDiagnostic(hookErr.Error())
	}
	if err := ctx.Err(); err != nil {
		result.Diagnostic = ""
		return result, err
	}
	result.ReviewFiles = review
	result.Report, result.TargetAliases = report, aliases
	result.Patch, result.PatchSummary = patch, summary
	return result, nil
}

// hostTranslationResult initializes a host translation result from evaluation output.
func hostTranslationResult(changes []change, report string, aliases []TargetAlias, evaluated bool) HostTranslation {
	files := len(changes)
	return HostTranslation{
		ReviewFiles:   reviewFiles(changes),
		Report:        report,
		TargetAliases: slices.Clone(aliases),
		Change:        HostChange{Files: files, AlreadySatisfied: evaluated && files == 0},
	}
}

// translateHostResult translates changes to a patch and updates the result.
func translateHostResult(ctx context.Context, changes []change, result *HostTranslation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	patch, err := translate(changes)
	if err != nil {
		return err
	}
	result.Patch = []byte(patch)
	result.PatchSummary = HostPatchSummary{Files: len(changes), Bytes: len(patch)}
	return nil
}

type filesystemWorkspace struct {
	root *os.Root
	cwd  string
}

// evaluateScript evaluates a script in the given workspace.
// evaluateScript evaluates a set of file edits in the given workspace.
func evaluateScript(ctx context.Context, workspace Workspace, edits []FileEdit) ([]change, filesystemWorkspace, string, []TargetAlias, error) {
	filesystem, err := validateWorkspace(ctx, workspace)
	if err != nil {
		return nil, filesystemWorkspace{}, "", nil, err
	}
	return evaluateScriptInFilesystem(ctx, filesystem, edits)
}

// evaluateScriptAt evaluates a set of file edits in the given directory.
func evaluateScriptAt(ctx context.Context, directory string, edits []FileEdit) ([]change, filesystemWorkspace, string, []TargetAlias, error) {
	filesystem, err := validateHostDirectory(ctx, directory)
	if err != nil {
		return nil, filesystemWorkspace{}, "", nil, err
	}
	return evaluateScriptInFilesystem(ctx, filesystem, edits)
}

// evaluateScriptInFilesystem evaluates file edits against a filesystem workspace.
func evaluateScriptInFilesystem(ctx context.Context, filesystem filesystemWorkspace, edits []FileEdit) ([]change, filesystemWorkspace, string, []TargetAlias, error) {
	program, err := parseFileEdits(edits)
	if err != nil {
		return nil, filesystemWorkspace{}, "", nil, err
	}
	load := func(path string) (loadedFile, error) {
		return filesystem.readFile(ctx, path)
	}
	changes, report, aliases, err := program.evaluate(ctx, filesystem.resolvePath, load)
	if err != nil {
		return nil, filesystemWorkspace{}, "", nil, err
	}
	return changes, filesystem, report, aliases, nil
}

// validateWorkspace validates and normalizes a workspace configuration.
func validateWorkspace(ctx context.Context, workspace Workspace) (filesystemWorkspace, error) {
	if ctx == nil {
		return filesystemWorkspace{}, fmt.Errorf("context is nil")
	}
	if err := ctx.Err(); err != nil {
		return filesystemWorkspace{}, err
	}
	if workspace.Root == nil {
		return filesystemWorkspace{}, fmt.Errorf("workspace root is nil")
	}
	cwd := workspace.CWD
	if cwd == "" {
		cwd = "."
	}
	cwd = filepath.Clean(cwd)
	if !filepath.IsLocal(cwd) {
		return filesystemWorkspace{}, fmt.Errorf("workspace cwd %q is not root-relative", workspace.CWD)
	}
	info, err := workspace.Root.Stat(cwd)
	if err != nil {
		return filesystemWorkspace{}, fmt.Errorf("validating workspace cwd %q: %w", cwd, err)
	}
	if !info.IsDir() {
		return filesystemWorkspace{}, fmt.Errorf("workspace cwd %q is not a directory", cwd)
	}
	return filesystemWorkspace{root: workspace.Root, cwd: cwd}, nil
}

// validateHostDirectory validates and normalizes a host directory path.
func validateHostDirectory(ctx context.Context, directory string) (filesystemWorkspace, error) {
	if ctx == nil {
		return filesystemWorkspace{}, fmt.Errorf("context is nil")
	}
	if err := ctx.Err(); err != nil {
		return filesystemWorkspace{}, err
	}
	if directory == "" {
		return filesystemWorkspace{}, nil
	}
	directory, err := filepath.Abs(directory)
	if err != nil {
		return filesystemWorkspace{}, fmt.Errorf("resolving host directory: %w", err)
	}
	directory = filepath.Clean(directory)
	info, err := os.Stat(directory)
	if err != nil {
		return filesystemWorkspace{}, fmt.Errorf("validating host directory %q: %w", directory, err)
	}
	if !info.IsDir() {
		return filesystemWorkspace{}, fmt.Errorf("host directory %q is not a directory", directory)
	}
	return filesystemWorkspace{cwd: directory}, nil
}

// resolvePath resolves a script path against the workspace root.
func (w filesystemWorkspace) resolvePath(path string) (string, error) {
	if w.root == nil {
		path = filepath.Clean(path)
		if !filepath.IsAbs(path) {
			if w.cwd == "" {
				return "", fmt.Errorf("relative path requires a host directory")
			}
			path = filepath.Join(w.cwd, path)
		}
		return filepath.Clean(path), nil
	}
	if filepath.IsAbs(path) {
		if !filepath.IsAbs(w.root.Name()) {
			return "", fmt.Errorf("absolute path requires an absolute workspace root")
		}
		relative, err := filepath.Rel(w.root.Name(), filepath.Clean(path))
		if err != nil {
			return "", fmt.Errorf("resolving path against workspace root: %w", err)
		}
		path = relative
	} else {
		path = filepath.Join(w.cwd, path)
	}
	path = filepath.Clean(path)
	if !filepath.IsLocal(path) {
		return "", fmt.Errorf("path resolves outside workspace root")
	}
	return path, nil
}

// hostPath converts a resolved path to a host filesystem path.
func (w filesystemWorkspace) hostPath(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(w.cwd, path)
}

// stat returns file information for the given path.
func (w filesystemWorkspace) stat(path string) (fs.FileInfo, error) {
	if w.root == nil {
		return os.Stat(w.hostPath(path))
	}
	return w.root.Stat(path)
}

// open opens the file at the given path.
func (w filesystemWorkspace) open(path string) (*os.File, error) {
	if w.root == nil {
		return os.Open(w.hostPath(path))
	}
	return w.root.Open(path)
}

// sanitizeDiagnostic sanitizes a diagnostic message for safe display.
func sanitizeDiagnostic(message string) string {
	var sanitized strings.Builder
	for _, character := range message {
		switch {
		case character == '\n':
			sanitized.WriteString("; ")
		case unicode.IsControl(character):
			escaped := strconv.QuoteRune(character)
			sanitized.WriteString(escaped[1 : len(escaped)-1])
		default:
			sanitized.WriteRune(character)
		}
	}
	return sanitized.String()
}

// failureDiagnostic formats a failure message as a diagnostic.
func failureDiagnostic(message string) string {
	return fmt.Sprintf("mekugi: %s\n", sanitizeDiagnostic(message))
}

// evaluationDiagnostic formats an evaluation error as a diagnostic with repair context.
func evaluationDiagnostic(ctx context.Context, err error, dataDirectory string) string {
	commands := commandsOf(err)
	if len(commands) == 0 {
		return failureDiagnostic(err.Error())
	}

	var output strings.Builder
	var diagnostic strings.Builder
	for _, command := range commands {
		diagnostic.WriteString(sanitizeDiagnostic(command.Error()))
		diagnostic.WriteByte('\n')
		diagnostic.WriteString(command.Repair)
	}
	output.WriteString(diagnostic.String())
	if _, routed := attemptMetadataFromContext(ctx); !routed {
		for _, hookErr := range runCommandErrorHooks(ctx, dataDirectory, commands, diagnostic.String(), errorHooksTimeout) {
			output.WriteString(warningDiagnostic(hookErr.Error()))
		}
	}
	return output.String()
}

// warningDiagnostic formats a warning message as a diagnostic.
func warningDiagnostic(message string) string {
	message = sanitizeDiagnostic(message)
	return fmt.Sprintf("mekugi: warning: %s\n", message)
}
