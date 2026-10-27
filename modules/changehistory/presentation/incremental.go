package presentation

import (
	"cmp"
	"maps"
	"slices"
)

// indexPaths admits only ordinary, same-path groups. Every historical endpoint
// reserves its path, including names a move or deletion no longer displays.
func (s *Snapshot) indexPaths() {
	s.paths = make(map[string]int)
	s.maxCaptureOrder = 0
	for i, file := range s.Publication.Files {
		position := i
		if file.Path == "" || len(file.Chunks) == 0 || slices.ContainsFunc(file.Chunks, func(chunk Chunk) bool {
			return chunk.Review.BeforePath != file.Path || chunk.Review.AfterPath != file.Path
		}) {
			position = -1
		}
		for _, chunk := range file.Chunks {
			s.maxCaptureOrder = max(s.maxCaptureOrder, chunk.CaptureOrder)
			for _, path := range []string{file.Path, chunk.Review.BeforePath, chunk.Review.AfterPath} {
				if path == "" {
					continue
				}
				if prior, found := s.paths[path]; found && prior != position {
					s.paths[path] = -1
				} else {
					s.paths[path] = position
				}
			}
		}
	}
}

// appendCaptures avoids regrouping retained history only when no old identity
// can change and all new effects follow the admitted capture order. Structural
// edits and late evidence continue through the canonical full-history path.
func (b *Builder) appendCaptures(captures []Chunk) bool {
	if b.base.paths == nil {
		return false
	}
	for _, chunk := range captures {
		path := chunk.Review.BeforePath
		if path == "" || chunk.Review.AfterPath != path || chunk.CaptureOrder < b.base.maxCaptureOrder {
			return false
		}
		if position, found := b.base.paths[path]; found && position < 0 {
			return false
		}
	}
	slices.SortStableFunc(captures, func(a, c Chunk) int {
		return cmp.Compare(a.CaptureOrder, c.CaptureOrder)
	})
	prior := b.base.Publication
	v := View{Files: slices.Clone(prior.Files), Visible: maps.Clone(prior.Unfiltered), Initialized: true}
	detached := make(map[int]bool)
	for i, file := range v.Files {
		visible := v.Visible[file.Key()]
		if !visible.Highlighted {
			continue
		}
		v.Files[i] = clearHighlights(file)
		detached[i] = true
		v.Visible[file.Key()] = clearHighlights(visible)
	}
	b.next.paths = maps.Clone(b.base.paths)
	callers := maps.Clone(prior.Counts.Callers)
	for _, chunk := range captures {
		path := chunk.Review.BeforePath
		position, found := b.next.paths[path]
		if !found {
			position = len(v.Files)
			b.next.paths[path] = position
			v.Files = append(v.Files, File{id: chunk.Key, Path: path})
			detached[position] = true
		}
		file := &v.Files[position]
		if !detached[position] {
			file.Chunks = slices.Clone(file.Chunks)
			detached[position] = true
		}
		chunk.Highlighted = true
		file.Chunks = append(file.Chunks, chunk)
		delete(v.Visible, file.Key())
		addCallerCounts(callers, chunk)
		b.next.maxCaptureOrder = max(b.next.maxCaptureOrder, chunk.CaptureOrder)
	}
	v.RefreshVisible()
	b.next.Publication = Publication{Files: v.Files, Unfiltered: v.Visible,
		Counts: Counts{Callers: callers, Net: NetCounts(&v)}, initialized: true}
	return true
}

func clearHighlights(file File) File {
	file.Highlighted = false
	file.Chunks = slices.Clone(file.Chunks)
	for i := range file.Chunks {
		file.Chunks[i].Highlighted = false
	}
	return file
}
