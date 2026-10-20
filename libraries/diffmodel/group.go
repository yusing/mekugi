package diffmodel

// Source: internal/livediff/model.go:315:357@[ebde9ad5]
// FileGroup follows a file through moves, removal, and recreation. Captures
// indexes the caller's ordered evidence; Path is its latest retained name.
type FileGroup struct {
	Path     string
	Captures []int
}

// GroupFiles groups supplied evidence in order without reordering or changing it.
func GroupFiles(captures []ReviewFile) []FileGroup {
	var files []FileGroup
	current, deleted := make(map[string]int), make(map[string]int)
	for at, file := range captures {
		path := file.BeforePath
		if path == "" {
			path = file.AfterPath
		}
		i, exists := current[path]
		if file.BeforePath == "" {
			if prior, found := deleted[path]; found {
				i, exists = prior, true
			}
		}
		if !exists {
			i = len(files)
			files = append(files, FileGroup{Path: path})
			current[path] = i
		}
		if file.BeforePath != file.AfterPath {
			delete(current, file.BeforePath)
			if file.AfterPath == "" {
				deleted[file.BeforePath] = i
			} else {
				current[file.AfterPath] = i
				delete(deleted, file.AfterPath)
				files[i].Path = file.AfterPath
			}
		}
		files[i].Captures = append(files[i].Captures, at)
	}
	return files
}
