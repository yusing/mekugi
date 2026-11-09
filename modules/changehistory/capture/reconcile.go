package capture

// Source: apps/mekugi/internal/router/exec_observation.go:38:1250@1e6d58c6.

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"

	"github.com/yusing/mekugi/libraries/diffmodel"
	changehistory "github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/changehistory/evidence"
)

// Comparison supplies concurrent exclusions and an eligible workspace fallback.
// A nil Workspaces compares only the named baseline. Claims come from prior
// completed windows covering the same physical paths and Git authority.
type Comparison struct {
	Evidence   *evidence.Source
	Excluded   []string
	Workspaces *Workspaces
	Workspace  string
	Claims     Claims
	// PriorChanges means this writer already has a captured changed interval.
	// An unchanged suffix must not turn the whole writer into an unknown no-op.
	PriorChanges bool
}

// Result separates file evidence from an unavailable optional workspace comparison.
// Durable consumers retain Files and Coverage even when SnapshotError is non-nil;
// provisional consumers may keep their previous completion boundary for retry.
// Outcome holds capture facts; callers supply host status and publication references.
type Result struct {
	Outcome
	Files []evidence.Review
	// Endpoints are the named states already read for this comparison. Other
	// open writers can resume from this boundary without a later file read.
	Endpoints     []File
	Complete      bool
	Claims        Claims
	SnapshotError error
}

// Reconcile compares named scope and an eligible workspace checkpoint without
// claiming or publishing the result. Callers serialize completion with claiming.
func Reconcile(ctx context.Context, observation Observation, comparison Comparison) Result {
	var result Result
	var snapshot []execSnapshotChange
	if comparison.Workspaces != nil {
		ctx, cancel := context.WithTimeout(ctx, workspaceSnapshotWait)
		defer cancel()
		checkpoints := maps.Clone(observation.Checkpoints)
		if observation.Tree != "" {
			if checkpoints == nil {
				checkpoints = make(map[string]string)
			}
			checkpoints[comparison.Workspace] = observation.Tree
		}
		seen := make(map[string]bool)
		budget := MaxContentBytes
		for _, directory := range slices.Sorted(maps.Keys(checkpoints)) {
			if checkpoints[directory] == "" {
				result.SnapshotError = errors.Join(result.SnapshotError, fmt.Errorf("no before checkpoint for %s", directory))
				continue
			}
			files, claims, err := comparison.Workspaces.since(ctx, directory, checkpoints[directory], comparison.Claims, comparison.Evidence)
			result.Claims = result.Claims.Merge(claims)
			result.SnapshotError = errors.Join(result.SnapshotError, err)
			for _, file := range files {
				if seen[file.physicalPath] {
					continue
				}
				seen[file.physicalPath] = true
				for _, endpoint := range []*File{&file.before, &file.after} {
					if comparison.Evidence == nil && len(endpoint.Content) > budget {
						endpoint.Content, endpoint.Error = "", "capture budget exhausted"
					} else {
						budget -= len(endpoint.Content)
					}
				}
				snapshot = append(snapshot, file)
			}
		}
	}
	interval := observation
	if comparison.PriorChanges {
		interval.RepeatedPaths = false
	}
	result.Files, result.Complete, result.Coverage, result.Endpoints = reconcile(ctx, interval, comparison.Excluded, snapshot, comparison.Evidence)
	if observation.Unresolved {
		result.Complete, result.Coverage = false, CoveragePartial
	}
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

func reconcile(ctx context.Context, observation Observation, excluded []string, snapshot []execSnapshotChange, source *evidence.Source) (reviews []evidence.Review, complete bool, coverage string, endpoints []File) {
	var changes []execSnapshotChange
	complete = true
	coverage = CoverageExact
	budget := MaxContentBytes
	reviewBudget := MaxContentBytes
	appendReview := func(file diffmodel.ReviewFile) {
		review := evidence.RetainReview(ctx, source, file, &reviewBudget)
		if review.Incomplete != "" {
			complete = false
		}
		reviews = append(reviews, review)
	}
	after := make(map[string]File, len(observation.Files))
	metadata := make(map[string]File, len(observation.Files))
	// Preserve leaf symlinks as filesystem entries. Resolving their parents
	// identifies directory aliases without conflating a link with its target.
	directories := make(map[string]string)
	var physicalDirectory func(string) string
	physicalDirectory = func(path string) string {
		if physical, found := directories[path]; found {
			return physical
		}
		physical, err := filepath.EvalSymlinks(path)
		if err != nil {
			physical = filepath.Clean(path)
			if parent := filepath.Dir(path); errors.Is(err, os.ErrNotExist) && parent != path {
				physical = filepath.Join(physicalDirectory(parent), filepath.Base(path))
			}
		}
		directories[path] = physical
		return physical
	}
	identity := func(path string) string {
		return filepath.Join(physicalDirectory(filepath.Dir(path)), filepath.Base(path))
	}
	roots := make(map[string]string)
	within := func(path, root string) bool {
		if changehistory.PathWithin(path, root) {
			return true
		}
		physicalRoot, found := roots[root]
		if !found {
			physicalRoot = identity(root)
			if info, err := os.Stat(root); err == nil && info.IsDir() {
				physicalRoot = physicalDirectory(root)
			}
			roots[root] = physicalRoot
		}
		return changehistory.PathWithin(identity(path), physicalRoot)
	}
	captured := make(map[string]bool, len(observation.Files))
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
			review := diffmodel.RenderIncompleteReviewFile(beforePath, afterPath, reason)
			if before.Error != "" && current.Error == "" && current.Kind == Text {
				// Keep the witnessed endpoint recoverable without presenting it
				// as an authored diff from an unknown before-state.
				text, err := current.Text()
				if err == nil {
					review = diffmodel.RenderUnbasedReviewFile(current.Path, text, reason)
				}
			}
			appendReview(review)
			complete = false
			return
		}
		if before.Kind == Other || current.Kind == Other {
			if before.Kind != current.Kind {
				appendReview(diffmodel.RenderIncompleteReviewFile(before.Path, before.Path, "not a regular file"))
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
		key := identity(before.Path)
		if captured[key] || slices.ContainsFunc(observation.Excluded, func(path string) bool { return within(before.Path, path) }) {
			return
		}
		captured[key] = true
		metadata[before.Path] = before
		if observation.endpoints == nil && before.Stamp != "" {
			// An uncaptured file with the same identity, size, and time was
			// not written.
			if info, err := os.Lstat(before.Path); err == nil && execFileStamp(info) == before.Stamp {
				after[before.Path] = before
				endpoints = append(endpoints, before)
				return
			}
		}
		current, supplied := observation.endpoints[before.Path]
		if !supplied {
			current = ReadFileIn(ctx, before.Path, &budget, source)
		}
		after[before.Path] = current
		endpoints = append(endpoints, current)
		record(before, current)
	}
	for _, before := range observation.Files {
		compare(before)
	}
	omitted := func(path string) bool {
		for _, omission := range observation.Omitted {
			if within(path, omission.Path) {
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
				appendReview(diffmodel.RenderIncompleteReviewFile(listing.Root, listing.Root, "destination listing exceeds its bound"))
				complete = false
				return filepath.SkipAll
			}
			relative, _ := filepath.Rel(listing.Root, path)
			seen[relative] = true
			if slices.ContainsFunc(observation.Excluded, func(root string) bool { return within(path, root) }) {
				return nil
			}
			if captured[identity(path)] {
				return nil
			}
			if stamp, listed := listing.Entries[relative]; listed {
				if info, err := entry.Info(); err != nil || execFileStamp(info) != stamp {
					appendReview(diffmodel.RenderIncompleteReviewFile(path, path, "modified; before content unavailable"))
					complete = false
				}
				return nil
			}
			compare(File{Path: path, Origin: listing.Origin})
			return nil
		})
		for relative := range listing.Entries {
			path := filepath.Join(listing.Root, relative)
			if slices.ContainsFunc(observation.Excluded, func(root string) bool { return within(path, root) }) {
				continue
			}
			if !captured[identity(path)] && !seen[relative] {
				_, err := os.Lstat(path)
				if err == nil {
					continue
				}
				if errors.Is(err, os.ErrNotExist) {
					appendReview(diffmodel.RenderIncompleteReviewFile(path, "", "deleted; before content unavailable"))
				} else {
					appendReview(diffmodel.RenderIncompleteReviewFile(path, path, "destination unavailable: "+err.Error()))
				}
				complete = false
			}
		}
	}
	// The workspace snapshot covers effects outside the named scope. Paths
	// that scope, a same-call patch, or an overlapping call claims stay theirs.
	claimed := func(path string) bool {
		if captured[identity(path)] || omitted(path) {
			return true
		}
		for _, roots := range [][]string{observation.Excluded, excluded} {
			for _, root := range roots {
				if within(path, root) {
					return true
				}
			}
		}
		for _, listing := range observation.Listings {
			if within(path, listing.Root) {
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
		if !FilePresent(deleted.before) || FilePresent(deleted.after) || deleted.before.Kind == Text && deleted.before.TextSize() == 0 {
			continue
		}
		for other, added := range changes {
			if paired[other] || FilePresent(added.before) || !FilePresent(added.after) || !SameContent(deleted.before, added.after) {
				continue
			}
			paired[index], paired[other] = true, true
			review := RenderReview(deleted.before.Path, added.after.Path, deleted.before, added.after)
			appendReview(review)
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
			copySource, captured := after[changed.before.CopyOf]
			if !captured && observation.endpoints == nil {
				copySource = ReadFileIn(ctx, changed.before.CopyOf, &budget, source)
			}
			if copySource.Error == "" && FilePresent(copySource) && SameContent(copySource, changed.after) {
				review.CopyFrom = changed.before.CopyOf
			}
		}
		appendReview(review)
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
				appendReview(review)
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
			current := ReadFileIn(ctx, omission.Path, &budget, source)
			var review diffmodel.ReviewFile
			switch {
			case current.Error != "":
				review = diffmodel.RenderIncompleteReviewFile(omission.Path, omission.Path, "managed baseline unavailable; "+current.Error)
			case current.Kind == Absent:
				review = diffmodel.RenderIncompleteReviewFile(omission.Path, "", "deleted; before content unavailable")
			case current.Kind == Text || current.Kind == Symlink:
				text, err := ReviewText(current)
				if err != nil {
					review = diffmodel.RenderIncompleteReviewFile(omission.Path, omission.Path, err.Error())
				} else {
					review = diffmodel.RenderUnbasedReviewFile(omission.Path, text, "managed baseline unavailable")
				}
			default:
				review = diffmodel.RenderIncompleteReviewFile(omission.Path, omission.Path, "managed baseline unavailable")
			}
			review.Origin = omission.Origin
			appendReview(review)
			deferredChanged = true
			continue
		}
		appendReview(diffmodel.RenderIncompleteReviewFile(omission.Path, omission.Path, omission.Reason))
		complete = false
	}
	if source == nil {
		reviews, complete = boundExecReviews(reviews, complete)
	}
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
	return reviews, complete, coverage, endpoints
}

// ReviewInterval closes only named paths with supplied observed endpoints. It
// reuses ordinary reconciliation, provenance and size bounds without rereading
// the filesystem or making a host-completion claim.
func ReviewInterval(before, endpoints []File, sources ...*evidence.Source) ([]evidence.Review, []File) {
	var source *evidence.Source
	if len(sources) > 0 {
		source = sources[0]
	}
	observation := Observation{endpoints: make(map[string]File)}
	var boundaries []File
	budget := MaxContentBytes
	for _, file := range before {
		for _, boundary := range endpoints {
			if file.Path != boundary.Path {
				continue
			}
			if source == nil && len(boundary.Content) > budget {
				boundary.Content, boundary.Error = "", "overlapping writer boundary exceeds capture bound"
			} else {
				budget -= len(boundary.Content)
			}
			observation.Files = append(observation.Files, file)
			observation.endpoints[file.Path] = boundary
			boundary.Origin, boundary.AlsoManaged = file.Origin, file.AlsoManaged
			boundaries = append(boundaries, boundary)
			break
		}
	}
	reviews, _, _, _ := reconcile(context.Background(), observation, nil, nil, source)
	return reviews, boundaries
}

// boundExecReviews keeps the encoded reviews within the derived record's
// bound, replacing the largest diffs with incomplete entries.
func boundExecReviews(reviews []evidence.Review, complete bool) ([]evidence.Review, bool) {
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
		reviews[largest] = evidence.Inline(diffmodel.RenderIncompleteReviewFile(review.BeforePath, review.AfterPath, "review exceeds the record bound"))
		size := len(responses.MustMarshalJSON(reviews[largest]))
		total -= sizes[largest] - size
		sizes[largest] = size
		complete = false
	}
	return reviews, complete
}
