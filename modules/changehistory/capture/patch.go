package capture

// Source: apps/mekugi/internal/router/native_apply_patch.go:27:511@1e6d58c6.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/yusing/mekugi/libraries/sourceanalysis/sourceparse"

	"github.com/yusing/mekugi/libraries/diffmodel"
)

const (
	MaxFileBytes             = 8 << 20
	maxNativePatchTotalBytes = 24 << 20
)

type PatchObservation struct {
	Input string
	Files []PatchFile
}

type PatchFile struct {
	BeforePath   string `json:",omitempty"`
	AfterPath    string `json:",omitempty"`
	Before       string `json:",omitempty"`
	Exists       bool   `json:",omitzero"`
	Error        string `json:",omitempty"`
	TargetBefore string `json:",omitempty"`
	TargetExists bool   `json:",omitzero"`
	TargetError  string `json:",omitempty"`
}

type nativePatchPath struct {
	before string
	after  string
}

// nativePatchPaths extracts only the path ownership declared by a complete
// stock apply_patch envelope. The host remains the parser and executor; this
// observer never uses its result to authorize or reproduce an edit.
func nativePatchPaths(input, workspace string) ([]nativePatchPath, error) {
	lines := strings.Split(strings.TrimSuffix(input, "\n"), "\n")
	if len(lines) < 3 || strings.TrimSuffix(lines[0], "\r") != "*** Begin Patch" ||
		strings.TrimSuffix(lines[len(lines)-1], "\r") != "*** End Patch" {
		return nil, errors.New("incomplete apply_patch envelope")
	}
	resolve := func(path string) (string, error) {
		path = strings.TrimSpace(path)
		if path == "" || strings.ContainsRune(path, '\x00') {
			return "", errors.New("invalid apply_patch path")
		}
		if !filepath.IsAbs(path) {
			if !filepath.IsAbs(workspace) {
				return "", errors.New("relative apply_patch path requires a workspace")
			}
			path = filepath.Join(workspace, path)
		}
		return filepath.Clean(path), nil
	}
	var paths []nativePatchPath
	for index := 1; index < len(lines)-1; index++ {
		line := strings.TrimSuffix(lines[index], "\r")
		var before, after string
		switch {
		case strings.HasPrefix(line, "*** Add File: "):
			after = strings.TrimPrefix(line, "*** Add File: ")
		case strings.HasPrefix(line, "*** Delete File: "):
			before = strings.TrimPrefix(line, "*** Delete File: ")
		case strings.HasPrefix(line, "*** Update File: "):
			before = strings.TrimPrefix(line, "*** Update File: ")
			after = before
			if index+1 < len(lines)-1 {
				next := strings.TrimSuffix(lines[index+1], "\r")
				if moved, ok := strings.CutPrefix(next, "*** Move to: "); ok {
					after = moved
					index++
				}
			}
		default:
			continue
		}
		var err error
		if before != "" {
			before, err = resolve(before)
			if err != nil {
				return nil, err
			}
		}
		if after != "" {
			after, err = resolve(after)
			if err != nil {
				return nil, err
			}
		}
		paths = append(paths, nativePatchPath{before: before, after: after})
	}
	if len(paths) == 0 {
		return nil, errors.New("apply_patch envelope has no file operation")
	}
	return paths, nil
}

func ReadPatchFile(path string) (content string, exists bool, err error) {
	if path == "" {
		return "", false, nil
	}
	file, err := sourceparse.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", false, err
	}
	if !info.Mode().IsRegular() || info.Size() > MaxFileBytes {
		return "", true, errors.New("file is not a bounded regular file")
	}
	content, err = sourceparse.ReadText(file, MaxFileBytes, "file exceeds capture capacity or is not UTF-8")
	return content, true, err
}

func Patch(input, workspace string) (PatchObservation, error) {
	paths, err := nativePatchPaths(input, workspace)
	if err != nil {
		return PatchObservation{}, err
	}
	observation := PatchObservation{Input: input, Files: make([]PatchFile, 0, len(paths))}
	total := len(input)
	for _, path := range paths {
		capturePath := path.before
		if capturePath == "" {
			capturePath = path.after
		}
		before, exists, captureErr := ReadPatchFile(capturePath)
		file := PatchFile{BeforePath: path.before, AfterPath: path.after, Before: before, Exists: exists}
		if path.before == "" && exists {
			file.BeforePath = capturePath
		}
		if captureErr != nil {
			file.Error = captureErr.Error()
		}
		if path.before != "" && path.after != "" && path.before != path.after {
			file.TargetBefore, file.TargetExists, captureErr = ReadPatchFile(path.after)
			if captureErr != nil {
				file.TargetError = captureErr.Error()
			}
		}
		total += len(file.BeforePath) + len(file.AfterPath) + len(file.Before) + len(file.Error) + len(file.TargetBefore) + len(file.TargetError)
		if total > maxNativePatchTotalBytes {
			return PatchObservation{}, errors.New("apply_patch baseline exceeds capture capacity")
		}
		observation.Files = append(observation.Files, file)
	}
	return observation, nil
}
func ReviewPatch(files []PatchFile) ([]diffmodel.ReviewFile, bool) {
	reviews := make([]diffmodel.ReviewFile, 0, len(files))
	complete := true
	appendDifference := func(pathBefore, pathAfter, before, after string, existedBefore, existsAfter bool) {
		if existedBefore == existsAfter && before == after && (pathBefore == pathAfter || !existedBefore) {
			return
		}
		if !existedBefore {
			pathBefore = ""
		}
		if !existsAfter {
			pathAfter = ""
		}
		reviews = append(reviews, diffmodel.RenderReviewFile(pathBefore, pathAfter, before, after))
	}
	for _, file := range files {
		readPath := file.AfterPath
		if readPath == "" {
			readPath = file.BeforePath
		}
		after, afterExists, afterErr := ReadPatchFile(readPath)
		move := file.BeforePath != "" && file.AfterPath != "" && file.BeforePath != file.AfterPath
		var sourceAfter string
		var sourceExists bool
		var sourceErr error
		if move {
			sourceAfter, sourceExists, sourceErr = ReadPatchFile(file.BeforePath)
		}
		if file.Error != "" || file.TargetError != "" || afterErr != nil || sourceErr != nil {
			reason := strings.Join([]string{file.Error, file.TargetError}, "; ")
			reason = strings.Trim(reason, "; ")
			for _, err := range []error{afterErr, sourceErr} {
				if err != nil {
					if reason != "" {
						reason += "; "
					}
					reason += err.Error()
				}
			}
			reviews = append(reviews, diffmodel.RenderIncompleteReviewFile(file.BeforePath, file.AfterPath, reason))
			complete = false
			continue
		}
		if !move {
			appendDifference(file.BeforePath, readPath, file.Before, after, file.Exists, afterExists)
			continue
		}
		if file.Exists && !sourceExists && !file.TargetExists && afterExists {
			appendDifference(file.BeforePath, file.AfterPath, file.Before, after, true, true)
			continue
		}
		appendDifference(file.BeforePath, file.BeforePath, file.Before, sourceAfter, file.Exists, sourceExists)
		appendDifference(file.AfterPath, file.AfterPath, file.TargetBefore, after, file.TargetExists, afterExists)
	}
	return reviews, complete
}

// Native patch baselines in a exec cell share one observation window, not
// intermediate per-tool states. Assign each path's window evidence once, to its
// first observation. A move with shared endpoints is recorded as independent
// source/target effects: the window cannot establish an intermediate rename.
func PatchWindowFiles(observations []PatchObservation) [][]PatchFile {
	uses := make(map[string]int)
	for _, observation := range observations {
		for _, file := range observation.Files {
			if file.BeforePath != "" {
				uses[file.BeforePath]++
			}
			if file.AfterPath != "" && file.AfterPath != file.BeforePath {
				uses[file.AfterPath]++
			}
		}
	}
	files := make([][]PatchFile, len(observations))
	seen := make(map[string]bool)
	appendPath := func(index int, path, before string, exists bool, reason string) {
		if path == "" || seen[path] {
			return
		}
		seen[path] = true
		files[index] = append(files[index], PatchFile{
			BeforePath: path, AfterPath: path, Before: before, Exists: exists, Error: reason,
		})
	}
	for index, observation := range observations {
		for _, file := range observation.Files {
			move := file.BeforePath != "" && file.AfterPath != "" && file.BeforePath != file.AfterPath
			if move && uses[file.BeforePath] == 1 && uses[file.AfterPath] == 1 {
				files[index] = append(files[index], file)
				seen[file.BeforePath], seen[file.AfterPath] = true, true
				continue
			}
			path := file.BeforePath
			if path == "" {
				path = file.AfterPath
			}
			appendPath(index, path, file.Before, file.Exists, file.Error)
			if move {
				appendPath(index, file.AfterPath, file.TargetBefore, file.TargetExists, file.TargetError)
			}
		}
	}
	return files
}

// Patches captures complete literal inputs under one aggregate baseline bound.
// Invalid or unavailable inputs never establish a pre-edit baseline.
func Patches(inputs []string, workspace string) []PatchObservation {
	observations := make([]PatchObservation, 0, len(inputs))
	total := 0
	for _, input := range inputs {
		observation, err := Patch(input, workspace)
		if err != nil {
			continue
		}
		for _, file := range observation.Files {
			total += len(file.Before) + len(file.BeforePath) + len(file.AfterPath) + len(file.Error) + len(file.TargetBefore) + len(file.TargetError)
		}
		if total > maxNativePatchTotalBytes {
			return nil
		}
		observations = append(observations, observation)
	}
	return observations
}
