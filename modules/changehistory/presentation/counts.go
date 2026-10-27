package presentation

// Source: internal/router/live_diff_terminal.go:504:562@[a42ac9f1]
import (
	"path/filepath"
	"slices"
	"strings"

	"github.com/yusing/mekugi/libraries/diffmodel"
)

// NetCounts reuses the saved view's composition and workspace scope.
// A caller filter affects navigation only, never the overall outcome. Only a
// filtered view needs another projection; the normal case reuses its cache.
func NetCounts(view *View) *diffmodel.Counts {
	all := *view
	if all.Caller != "" {
		all.Caller, all.Visible = "", nil
	}
	all.RefreshVisible()
	var total *diffmodel.Counts
	for _, file := range all.Files {
		if !slices.ContainsFunc(file.Chunks, projectCapture) {
			continue
		}
		if total == nil {
			total = new(diffmodel.Counts)
		}
		count := all.Visible[file.Key()].NetCounts()
		if count.Added < 0 || count.Removed < 0 {
			return new(diffmodel.Counts{Added: -1, Removed: -1})
		}
		total.Added += count.Added
		total.Removed += count.Removed
	}
	return total
}

func projectCapture(chunk Chunk) bool {
	review := chunk.Review
	return chunk.Workspace == "" || review.Origin != "" ||
		slices.ContainsFunc([]string{review.BeforePath, review.AfterPath}, func(path string) bool {
			return path != "" && (path == chunk.Workspace || strings.HasPrefix(path, strings.TrimSuffix(chunk.Workspace, string(filepath.Separator))+string(filepath.Separator)))
		})
}

// CallerCounts totals each caller's captured edit lines, regardless of
// the caller filter. Incomplete captures contribute no counts, rather than
// hiding confirmed edits from the same caller behind an unknown total.
// Files outside the capturing workspace, such as rewritten scratch files, are
// not project edits and stay out of the totals.
func CallerCounts(files []File) map[string]diffmodel.Counts {
	counts := make(map[string]diffmodel.Counts)
	for _, file := range files {
		for _, chunk := range file.Chunks {
			addCallerCounts(counts, chunk)
		}
	}
	return counts
}

func addCallerCounts(counts map[string]diffmodel.Counts, chunk Chunk) {
	added, removed := chunk.Review.LineCounts()
	if added < 0 || removed < 0 || !projectCapture(chunk) {
		return
	}
	key := CallerKey(chunk.Caller)
	count := counts[key]
	count.Added, count.Removed = count.Added+added, count.Removed+removed
	counts[key] = count
}
