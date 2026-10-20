package capture

// Source: apps/mekugi/internal/router/exec_observation.go:38:1250@1e6d58c6.

import (
	"cmp"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"

	"github.com/yusing/mekugi/libraries/diffmodel"
	changehistory "github.com/yusing/mekugi/modules/changehistory/changes"
)

// Comparison supplies concurrent exclusions and an eligible workspace fallback.
// A nil Workspaces compares only the named baseline. Claims come from prior
// completed windows in the same snapshot domain.
type Comparison struct {
	Excluded   []string
	Workspaces *Workspaces
	Workspace  string
	Claims     Claims
}

// Result separates file evidence from an unavailable optional workspace comparison.
// Durable consumers retain Files and Coverage even when SnapshotError is non-nil;
// provisional consumers may keep their previous completion boundary for retry.
// Outcome holds capture facts; callers supply host status and publication references.
type Result struct {
	Outcome
	Files         []diffmodel.ReviewFile
	Complete      bool
	Claims        Claims
	SnapshotError error
}

// Reconcile compares named scope and an eligible workspace checkpoint without
// claiming or publishing the result. Callers serialize completion with claiming.
func Reconcile(ctx context.Context, observation Observation, comparison Comparison) Result {
	var result Result
	var snapshot []execSnapshotChange
	if comparison.Workspaces != nil && observation.Tree != "" {
		snapshot, result.Claims, result.SnapshotError = comparison.Workspaces.since(ctx, comparison.Workspace, observation.Tree, comparison.Claims)
	}
	result.Files, result.Complete, result.Coverage = reconcile(observation, comparison.Excluded, snapshot)
	if result.SnapshotError != nil && result.Coverage == CoverageExact {
		result.Coverage = CoveragePartial
	}
	result.Class, result.Labels, result.CodeMode = observation.Class, observation.Labels, observation.CodeMode
	result.Scope, result.ScopeReason = observation.ScopePaths(), observation.Reason
	for _, listing := range observation.Listings {
		if listing.Entries == nil {
			continue
		}
		if _, err := os.Lstat(listing.Root); errors.Is(err, os.ErrNotExist) {
			result.DeletedDirectories = AddRoot(result.DeletedDirectories, listing.Root)
		}
	}
	if result.SnapshotError != nil {
		result.ScopeReason = strings.TrimPrefix(result.ScopeReason+"; workspace snapshot unavailable: "+result.SnapshotError.Error(), "; ")
	}
	if observation.RepeatedPaths {
		result.ScopeReason = strings.TrimPrefix(result.ScopeReason+"; intermediate writes unobserved (call-window comparison only)", "; ")
	}
	return result
}

func reconcile(observation Observation, excluded []string, snapshot []execSnapshotChange) (reviews []diffmodel.ReviewFile, complete bool, coverage string) {
	var changes []execSnapshotChange
	complete = true
	coverage = CoverageExact
	budget := MaxContentBytes
	after := make(map[string]File, len(observation.Files))
	metadata := make(map[string]File, len(observation.Files))
	// record keeps one path's before/after evidence as a change, or as an
	// incomplete review when either side could not be read.
	record := func(before, current File) {
		if before.Error != "" || current.Error != "" {
			beforePath, afterPath := before.Path, before.Path
			if !FilePresent(before) && before.Error == "" {
				beforePath = ""
			}
			if !FilePresent(current) && current.Error == "" {
				afterPath = ""
			}
			reason := strings.Trim(strings.Join([]string{before.Error, current.Error}, "; "), "; ")
			reviews = append(reviews, diffmodel.RenderIncompleteReviewFile(beforePath, afterPath, reason))
			complete = false
			return
		}
		if before.Kind == Other || current.Kind == Other {
			if before.Kind != current.Kind {
				reviews = append(reviews, diffmodel.RenderIncompleteReviewFile(before.Path, before.Path, "not a regular file"))
				complete = false
			}
			return
		}
		if !FilePresent(before) && !FilePresent(current) || SameContent(before, current) {
			return
		}
		changes = append(changes, execSnapshotChange{before: before, after: current})
	}
	compare := func(before File) {
		if _, seen := after[before.Path]; seen || slices.Contains(observation.Excluded, before.Path) {
			return
		}
		metadata[before.Path] = before
		if observation.endpoints == nil && before.Stamp != "" {
			// An uncaptured file with the same identity, size, and time was
			// not written.
			if info, err := os.Lstat(before.Path); err == nil && execFileStamp(info) == before.Stamp {
				after[before.Path] = before
				return
			}
		}
		current, supplied := observation.endpoints[before.Path]
		if !supplied {
			current = ReadFile(before.Path, &budget)
		}
		after[before.Path] = current
		record(before, current)
	}
	for _, before := range observation.Files {
		compare(before)
	}
	omitted := func(path string) bool {
		for _, omission := range observation.Omitted {
			if changehistory.PathWithin(path, omission.Path) {
				return true
			}
		}
		return false
	}
	walked := 0
	for _, listing := range observation.Listings {
		// A cut listing cannot prove which files are new; its omission
		// already marks the record incomplete.
		if omitted(listing.Root) {
			continue
		}
		seen := make(map[string]bool)
		_ = filepath.WalkDir(listing.Root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() || path == listing.Root {
				return nil
			}
			if walked++; walked > maxExecListingEntries {
				reviews = append(reviews, diffmodel.RenderIncompleteReviewFile(listing.Root, listing.Root, "destination listing exceeds its bound"))
				complete = false
				return filepath.SkipAll
			}
			relative, _ := filepath.Rel(listing.Root, path)
			seen[relative] = true
			if slices.Contains(observation.Excluded, path) {
				return nil
			}
			if _, captured := after[path]; captured {
				return nil
			}
			if stamp, listed := listing.Entries[relative]; listed {
				if info, err := entry.Info(); err != nil || execFileStamp(info) != stamp {
					reviews = append(reviews, diffmodel.RenderIncompleteReviewFile(path, path, "modified; before content unavailable"))
					complete = false
				}
				return nil
			}
			compare(File{Path: path, Origin: listing.Origin})
			return nil
		})
		for relative := range listing.Entries {
			path := filepath.Join(listing.Root, relative)
			if slices.Contains(observation.Excluded, path) {
				continue
			}
			if _, captured := after[path]; !captured && !seen[relative] {
				_, err := os.Lstat(path)
				if err == nil {
					continue
				}
				if errors.Is(err, os.ErrNotExist) {
					reviews = append(reviews, diffmodel.RenderIncompleteReviewFile(path, "", "deleted; before content unavailable"))
				} else {
					reviews = append(reviews, diffmodel.RenderIncompleteReviewFile(path, path, "destination unavailable: "+err.Error()))
				}
				complete = false
			}
		}
	}
	// The workspace snapshot covers effects outside the named scope. Paths
	// that scope, a same-call patch, or an overlapping call claims stay theirs.
	claimed := func(path string) bool {
		if _, captured := after[path]; captured || omitted(path) {
			return true
		}
		for _, roots := range [][]string{observation.Excluded, excluded} {
			for _, root := range roots {
				if changehistory.PathWithin(path, root) {
					return true
				}
			}
		}
		for _, listing := range observation.Listings {
			if changehistory.PathWithin(path, listing.Root) {
				return true
			}
		}
		return false
	}
	vcsOrigin := ""
	for _, program := range observation.Programs {
		if program.VCS {
			vcsOrigin = program.Label
			break
		}
	}
	for _, snapshot := range snapshot {
		if !claimed(snapshot.before.Path) {
			// A mixed call has no per-write facts. Only source-named sibling
			// edits can be separated from VCS effects; keep the rest in history.
			snapshot.before.Origin = vcsOrigin
			metadata[snapshot.before.Path] = snapshot.before
			record(snapshot.before, snapshot.after)
		}
	}
	// A deletion and an addition with identical content form one move. Empty
	// files carry no evidence of which deletion an addition came from.
	paired := make(map[int]bool)
	for index, deleted := range changes {
		if !FilePresent(deleted.before) || FilePresent(deleted.after) || deleted.before.Kind == Text && deleted.before.Content == "" {
			continue
		}
		for other, added := range changes {
			if paired[other] || FilePresent(added.before) || !FilePresent(added.after) || !SameContent(deleted.before, added.after) {
				continue
			}
			paired[index], paired[other] = true, true
			review := RenderReview(deleted.before.Path, added.after.Path, deleted.before, added.after)
			reviews = append(reviews, review)
			break
		}
	}
	for index, changed := range changes {
		if paired[index] {
			continue
		}
		beforePath, afterPath := changed.before.Path, changed.after.Path
		if !FilePresent(changed.before) {
			beforePath = ""
		}
		if !FilePresent(changed.after) {
			afterPath = ""
		}
		review := RenderReview(beforePath, afterPath, changed.before, changed.after)
		if beforePath == "" && changed.before.CopyOf != "" {
			// A created copy names its source when the content matches.
			source, captured := after[changed.before.CopyOf]
			if !captured && observation.endpoints == nil {
				source = ReadFile(changed.before.CopyOf, &budget)
			}
			if source.Error == "" && FilePresent(source) && SameContent(source, changed.after) {
				review.CopyFrom = changed.before.CopyOf
			}
		}
		reviews = append(reviews, review)
	}
	deferredChanged := false
	for _, omission := range observation.Omitted {
		if slices.Contains(observation.Excluded, omission.Path) || slices.Contains(excluded, omission.Path) {
			continue
		}
		if omission.Deferred {
			// A remote mount or missing clock cannot prove an unchanged path.
			// Keep the named gap instead of comparing timestamps from another host.
			if observation.WindowStart.IsZero() || execRemoteFilesystem(filepath.Dir(omission.Path)) {
				review := diffmodel.RenderIncompleteReviewFile(omission.Path, omission.Path, "managed baseline unavailable; filesystem clock incomparable")
				review.Origin = omission.Origin
				reviews = append(reviews, review)
				complete = false
				deferredChanged = true
				continue
			}
			changed, ok := execFileTimes(omission.Path)
			if ok && changed.Before(observation.WindowStart) {
				continue
			}
			// These paths existed during provider enumeration. A missing
			// after-state is therefore a possible deletion, not a no-op.
			current := ReadFile(omission.Path, &budget)
			var review diffmodel.ReviewFile
			switch {
			case current.Error != "":
				review = diffmodel.RenderIncompleteReviewFile(omission.Path, omission.Path, "managed baseline unavailable; "+current.Error)
			case current.Kind == Absent:
				review = diffmodel.RenderIncompleteReviewFile(omission.Path, "", "deleted; before content unavailable")
			case current.Kind == Text || current.Kind == Symlink:
				review = diffmodel.RenderUnbasedReviewFile(omission.Path, ReviewText(current), "managed baseline unavailable")
			default:
				review = diffmodel.RenderIncompleteReviewFile(omission.Path, omission.Path, "managed baseline unavailable")
			}
			review.Origin = omission.Origin
			reviews = append(reviews, review)
			deferredChanged = true
			continue
		}
		reviews = append(reviews, diffmodel.RenderIncompleteReviewFile(omission.Path, omission.Path, omission.Reason))
		complete = false
	}
	reviews, complete = boundExecReviews(reviews, complete)
	for i := range reviews {
		file := &reviews[i]
		meta, ok := metadata[cmp.Or(file.AfterPath, file.BeforePath)]
		if !ok {
			meta, ok = metadata[file.BeforePath]
		}
		if ok {
			if before, found := metadata[file.BeforePath]; found && before.Origin == "" {
				meta = before
			}
			file.Origin = meta.Origin
			if meta.AlsoManaged != "" {
				file.OriginNote = meta.AlsoManaged + changehistory.SharedOriginSuffix
			}
		} else {
			for _, omission := range observation.Omitted {
				if omission.Path == file.BeforePath || omission.Path == file.AfterPath {
					file.Origin = omission.Origin
					break
				}
			}
		}
	}
	if (!complete || deferredChanged) && coverage == CoverageExact {
		coverage = CoveragePartial
	}
	if observation.Reason == "capture deadline" {
		complete, coverage = false, CoveragePartial
	}
	if observation.RepeatedPaths && len(reviews) == 0 {
		// Before/after equality is not intermediate edit history. Keep a
		// readable incomplete attempt, without inventing intermediate bytes.
		complete, coverage = false, CoveragePartial
	}
	return reviews, complete, coverage
}

// ReviewInterval closes only named paths with supplied observed endpoints. It
// reuses ordinary reconciliation, provenance and size bounds without rereading
// the filesystem or making a host-completion claim.
func ReviewInterval(before, endpoints []File) ([]diffmodel.ReviewFile, []File) {
	observation := Observation{endpoints: make(map[string]File)}
	var boundaries []File
	budget := MaxContentBytes
	for _, file := range before {
		for _, boundary := range endpoints {
			if file.Path != boundary.Path {
				continue
			}
			if len(boundary.Content) > budget {
				boundary.Content, boundary.Error = "", "overlapping writer boundary exceeds capture bound"
			} else {
				budget -= len(boundary.Content)
			}
			observation.Files = append(observation.Files, file)
			observation.endpoints[file.Path] = boundary
			boundaries = append(boundaries, boundary)
			break
		}
	}
	reviews, _, _ := reconcile(observation, nil, nil)
	return reviews, boundaries
}

// boundExecReviews keeps the encoded reviews within the derived record's
// bound, replacing the largest diffs with incomplete entries.
func boundExecReviews(reviews []diffmodel.ReviewFile, complete bool) ([]diffmodel.ReviewFile, bool) {
	sizes := make([]int, len(reviews))
	total := 0
	for index, review := range reviews {
		sizes[index] = len(responses.MustMarshalJSON(review))
		total += sizes[index]
	}
	for total > maxExecRecordBytes {
		largest := 0
		for index := range sizes {
			if sizes[index] > sizes[largest] {
				largest = index
			}
		}
		review := reviews[largest]
		reviews[largest] = diffmodel.RenderIncompleteReviewFile(review.BeforePath, review.AfterPath, "review exceeds the record bound")
		size := len(responses.MustMarshalJSON(reviews[largest]))
		total -= sizes[largest] - size
		sizes[largest] = size
		complete = false
	}
	return reviews, complete
}
