package presentation

// Source: internal/ui/diffview/navigation.go:46:192@[ebde9ad5]
import (
	"cmp"
	"maps"
	"slices"
	"strings"

	"github.com/yusing/mekugi/libraries/pathdisplay"
)

type FileTree struct {
	Flat          bool
	Query         string
	Collapsed     map[string]bool
	Entries       []NavEntry
	Matches       []int
	Cursor, Total int
	Workspace     string
}

type NavEntry struct {
	ID, Label, Folder  string
	File, Depth, Count int
}

type liveDiffNavNode struct {
	path    string
	folders map[string]*liveDiffNavNode
	files   []int
	count   int
}

func (n *FileTree) Rebuild(files []File, workspace string) {
	cursorID := ""
	if n.Cursor < len(n.Entries) {
		cursorID = n.Entries[n.Cursor].ID
	}
	n.Entries, n.Matches, n.Total, n.Workspace = nil, nil, 0, workspace
	paths := make(map[int]string)
	for i, file := range files {
		if len(file.Chunks) == 0 {
			continue
		}
		n.Total++
		path := pathdisplay.ForWorkspace(workspace, file.Path)
		if !strings.Contains(strings.ToLower(path), strings.ToLower(n.Query)) {
			continue
		}
		paths[i] = path
		n.Matches = append(n.Matches, i)
	}
	slices.SortStableFunc(n.Matches, func(a, b int) int {
		if !n.Flat && n.Query == "" {
			left, right := strings.Split(paths[a], "/"), strings.Split(paths[b], "/")
			for i := 0; i < min(len(left), len(right)); i++ {
				aDir, bDir := i < len(left)-1, i < len(right)-1
				if aDir != bDir {
					if aDir {
						return -1
					}
					return 1
				}
				if order := cmp.Compare(left[i], right[i]); order != 0 {
					return order
				}
			}
		}
		return cmp.Compare(paths[a], paths[b])
	})
	root := &liveDiffNavNode{folders: map[string]*liveDiffNavNode{}}
	for _, i := range n.Matches {
		if n.Flat || n.Query != "" {
			n.Entries = append(n.Entries, NavEntry{ID: "f:" + files[i].Key(), Label: paths[i], File: i})
			continue
		}
		node := root
		parts := strings.Split(paths[i], "/")
		for j, part := range parts[:len(parts)-1] {
			child := node.folders[part]
			if child == nil {
				folder := strings.Join(parts[:j+1], "/")
				if folder == "" {
					folder = "/"
				}
				child = &liveDiffNavNode{path: folder, folders: map[string]*liveDiffNavNode{}}
				node.folders[part] = child
			}
			child.count++
			node = child
		}
		node.files = append(node.files, i)
	}
	var walk func(*liveDiffNavNode, int)
	walk = func(node *liveDiffNavNode, depth int) {
		names := slices.Sorted(maps.Keys(node.folders))
		for _, name := range names {
			child, label := node.folders[name], name
			if label == "" {
				label = "/"
			}
			// Compact chains without flattening branches or losing collapse identity.
			for len(child.folders) == 1 && len(child.files) == 0 {
				for segment, next := range child.folders {
					label = strings.TrimSuffix(label, "/") + "/" + segment
					child = next
				}
			}
			n.Entries = append(n.Entries, NavEntry{ID: "d:" + child.path, Label: label, Folder: child.path, File: -1, Depth: depth, Count: child.count})
			if !n.Collapsed[child.path] {
				walk(child, depth+1)
			}
		}
		for _, i := range node.files {
			_, name, found := strings.CutLast(paths[i], "/")
			if !found {
				name = paths[i]
			}
			n.Entries = append(n.Entries, NavEntry{ID: "f:" + files[i].Key(), Label: name, File: i, Depth: depth})
		}
	}
	if !n.Flat && n.Query == "" {
		walk(root, 0)
	}
	n.Cursor = min(n.Cursor, max(0, len(n.Entries)-1))
	for i, entry := range n.Entries {
		if entry.ID == cursorID {
			n.Cursor = i
		}
	}
}
