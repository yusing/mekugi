package router

import (
	"path/filepath"
	"strings"

	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/session"
	"github.com/yusing/mekugi/internal/ui/diffview"
)

type runtimePreview struct {
	path, before string
	exists       bool
	err          error
	preview      diffview.Preview
}

// Native adapters supply decoded intent. This shared presentation path reuses
// bounded source observation and review rendering, never a capture or executor.
func (u *appServerUI) runtimePreview(e session.Event) {
	if e.Edit == nil || e.Historical {
		return
	}
	r := u.runtime
	if r.previews == nil {
		r.previews = make(map[string]*runtimePreview)
	}
	p := r.previews[e.ID]
	path := e.Edit.Path
	if !filepath.IsAbs(path) {
		path = filepath.Join(u.session.cwd, path)
	}
	path = filepath.Clean(path)
	if p == nil {
		if len(r.previews) >= 128 {
			return
		}
		p = &runtimePreview{path: path}
		p.before, p.exists, p.err = liveDiffPreviewFile(path)
		r.previews[e.ID] = p
	}
	caller := "/root"
	if e.Caller != "" {
		caller = "native/" + e.Caller
	}
	preview := diffview.Preview{ID: e.ID, Workspace: u.session.cwd, Thread: u.thread, Caller: caller,
		Tool: e.Role, Status: diffview.PreviewEdit, Footer: "Proposed input · not saved edit evidence"}
	switch {
	case path != p.path:
		preview.Status = diffview.PreviewUnavailable + "edit target changed during input"
	case p.err != nil:
		preview.Status = diffview.PreviewUnavailable + p.err.Error()
	case e.Edit.Partial:
		// A received prefix cannot establish a deletion or replacement of the
		// unseen suffix. Show only incoming lines, clearly marked provisional.
		preview.Files = []mekugi.ReviewFile{mekugi.RenderReviewPreviewFile("", path, "", e.Edit.Content)}
		preview.Footer = "Incoming content prefix · replacement not yet known"
	default:
		after := e.Edit.Content
		if e.Edit.Replace {
			count := strings.Count(p.before, e.Edit.Old)
			if !p.exists || count == 0 || count > 1 && !e.Edit.ReplaceAll {
				preview.Status = diffview.PreviewUnavailable + "edit operand has no unique observed match"
				break
			}
			n := 1
			if e.Edit.ReplaceAll {
				n = -1
			}
			after = strings.Replace(p.before, e.Edit.Old, e.Edit.Content, n)
		}
		beforePath := path
		if !p.exists {
			beforePath = ""
		}
		preview.Files = []mekugi.ReviewFile{mekugi.RenderReviewPreviewFile(beforePath, path, p.before, after)}
	}
	p.preview = preview
	u.shell.preview(preview)
	u.shell.diff.previewPane.Update(preview)
	u.shell.diff.dirty = true
}

func (u *appServerUI) settleRuntimePreview(id string, failed bool) {
	p := u.runtime.previews[id]
	if p == nil {
		return
	}
	preview := p.preview
	preview.Complete = true
	preview.Footer = "Native tool ended · proposal only, no saved capture"
	if failed {
		preview.Footer = "Native tool failed or was denied · proposal not confirmed"
	}
	u.shell.preview(preview)
	u.shell.diff.previewPane.Update(preview)
	u.shell.diff.dirty = true
	delete(u.runtime.previews, id)
}
