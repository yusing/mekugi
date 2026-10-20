package presentation

// Source: internal/ui/diffview/changes.go:22:196@[ebde9ad5]
import (
	"cmp"
	"slices"
	"strings"

	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/pathdisplay"
)

type Changes struct {
	Query string
	Nodes []ChangeNode
	Paths []string // File names by view index.
	// workspace displays rename sources.
	Workspace string
	Expanded  map[string]bool
	// target is the change and file that { } last opened.
	Target ChangeTarget
}

type ChangeNode struct {
	Origin
	Files   []ChangeFile
	Added   int
	Removed int
	Unknown bool
}

type ChangeFile struct {
	File           int
	Added, Removed int
	Unknown        bool
	Status         diffmodel.Status
}

type ChangeTarget struct{ Change, File string }

// Callers lists the caller filter keys with captures, main first.
func Callers(view *View) []string {
	var callers []string
	for _, file := range view.Files {
		for _, chunk := range file.Chunks {
			if key := CallerKey(chunk.Caller); !slices.Contains(callers, key) {
				callers = append(callers, key)
			}
		}
	}
	if i := slices.Index(callers, "/root"); i > 0 {
		callers = slices.Insert(slices.Delete(callers, i, i+1), 0, "/root")
	}
	return callers
}

func (l *Changes) Rebuild(view *View, workspace string, callerName func(string) string) {
	l.Nodes, l.Paths, l.Workspace = nil, nil, workspace
	for _, file := range view.Files {
		path := pathdisplay.ForWorkspace(workspace, file.Path)
		if _, name, found := strings.CutLast(path, "/"); found {
			path = name
		}
		l.Paths = append(l.Paths, path)
	}
	index := make(map[string]int)
	type capture struct {
		chunk Chunk
		file  int
	}
	var captures []capture
	for i, file := range view.Files {
		for _, chunk := range file.Chunks {
			if view.Shows(chunk) {
				captures = append(captures, capture{chunk, i})
			}
		}
	}
	slices.SortStableFunc(captures, func(a, b capture) int {
		return cmp.Compare(a.chunk.CaptureOrder, b.chunk.CaptureOrder)
	})
	query := strings.ToLower(l.Query)
	for _, capture := range captures {
		chunk := capture.chunk
		if query != "" && !changeMatches(chunk.Origin, query, callerName) {
			continue
		}
		id := chunk.Change
		if id == "" {
			id = chunk.Key
		}
		n, found := index[id]
		if !found {
			n = len(l.Nodes)
			index[id] = n
			l.Nodes = append(l.Nodes, ChangeNode{Origin: chunk.Origin})
			if l.Nodes[n].Change == "" {
				l.Nodes[n].Change = "change"
			}
		}
		node := &l.Nodes[n]
		added, removed := chunk.Review.LineCounts()
		incomplete := chunk.Review.Incomplete != ""
		node.Unknown = node.Unknown || incomplete
		node.Added, node.Removed = node.Added+added, node.Removed+removed
		at := slices.IndexFunc(node.Files, func(file ChangeFile) bool { return file.File == capture.file })
		if at < 0 {
			at = len(node.Files)
			node.Files = append(node.Files, ChangeFile{File: capture.file, Status: diffmodel.Status{Before: chunk.Review.BeforePath}})
		}
		file := &node.Files[at]
		file.Added, file.Removed, file.Unknown = file.Added+added, file.Removed+removed, file.Unknown || incomplete
		file.Status.Add(chunk.Review)
	}
}

// changeMatches filters by change ID, caller, or source; @name selects a caller.
func changeMatches(origin Origin, query string, callerName func(string) string) bool {
	caller := "unknown"
	if origin.Caller != "" {
		caller = strings.ToLower(callerName(origin.Caller))
	}
	if name, ok := strings.CutPrefix(query, "@"); ok {
		return strings.Contains(caller, name)
	}
	return strings.Contains(strings.ToLower(origin.Change+" "+origin.Source)+" "+caller, query)
}
