package launch

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/creack/pty"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/libraries/persistence"
	"github.com/yusing/mekugi/libraries/terminalui"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

type imagePreviewResult struct {
	scope      terminalsession.SourceContext
	generation uint64
	source     terminalui.ImageSource
	preview    *terminalui.ImagePreview
}

// imagePreviews belongs to one physical frontend, shared by live attachments or
// replay seeks. At most one storage/decode job runs. Retirement never joins that
// job on the terminal loop; cancellation releases its eventual result instead.
type imagePreviews struct {
	scope      terminalsession.SourceContext
	generation uint64
	images     map[terminalui.ImageSource]*terminalui.ImagePreview
	order      []terminalui.ImageSource
	protocol   terminalui.ImageProtocol
	pending    bool
	results    chan imagePreviewResult
	cancel     context.CancelFunc
	ctx        context.Context
	dialog     terminalui.ImageSource
	reading    terminalui.ImageSource
	cancelRead context.CancelFunc
}

func (p *imagePreviews) reset() {
	if p.cancelRead != nil {
		p.cancelRead()
	}
	for _, image := range p.images {
		image.Close()
	}
	p.images, p.order, p.scope = make(map[terminalui.ImageSource]*terminalui.ImagePreview), nil, terminalsession.SourceContext{}
	p.dialog = terminalui.ImageSource{}
	p.generation++
}

func (p *imagePreviews) close() {
	if p.cancel != nil {
		p.cancel()
	}
	p.reset()
}

func (p *imagePreviews) prepare(stdout *os.File, terminal *terminalsession.Attachment, scope terminalsession.SourceContext) {
	if p.scope != scope || p.images == nil {
		p.reset()
		p.scope = scope
	}
	protocol := terminalui.DetectImages(os.Getenv)
	cellWidth, cellHeight := 0, 0
	if protocol == terminalui.SixelImages {
		if size, err := pty.GetsizeFull(stdout); err == nil && size.Cols > 0 && size.Rows > 0 {
			cellWidth, cellHeight = int(size.X)/int(size.Cols), int(size.Y)/int(size.Rows)
		}
		if cellWidth <= 0 || cellHeight <= 0 {
			protocol = terminalui.NoImages
		}
	}
	p.protocol = protocol
	for _, target := range []*activityui.View{terminal.Transcript, terminal.Agents} {
		target.ImageProtocol, target.ImageCellWidth, target.ImageCellHeight = protocol, cellWidth, cellHeight
		target.ImagePreviews = p.images
	}
	var dialog terminalui.ImageSource
	if terminal.Dialog != nil {
		dialog = terminal.Dialog.ImageSource()
	}
	if dialog != p.dialog {
		if p.reading.Dialog != 0 && p.cancelRead != nil {
			p.cancelRead()
		}
		p.images[p.dialog].Close()
		delete(p.images, p.dialog)
		p.order = slices.DeleteFunc(p.order, func(source terminalui.ImageSource) bool { return source == p.dialog })
		p.dialog = dialog
	}
	if terminal.Dialog != nil {
		terminal.Dialog.Image, terminal.Dialog.ImageReady = p.images[dialog]
	}
}

func (p *imagePreviews) request(ctx context.Context, scope terminalsession.SourceContext, paths []terminalui.ImageSource) {
	if p.scope != scope || p.pending || ctx.Err() != nil {
		return
	}
	if p.results == nil {
		p.results = make(chan imagePreviewResult)
		p.ctx, p.cancel = context.WithCancel(ctx)
	}
	for _, path := range paths {
		if _, ok := p.images[path]; ok {
			continue
		}
		if len(p.order) >= 64 {
			index := slices.IndexFunc(p.order, func(candidate terminalui.ImageSource) bool { return !slices.Contains(paths, candidate) })
			if index < 0 {
				return
			}
			p.images[p.order[index]].Close()
			delete(p.images, p.order[index])
			p.order = slices.Delete(p.order, index, index+1)
		}
		p.pending = true
		results, workerContext, generation, protocol := p.results, p.ctx, p.generation, p.protocol
		readContext, cancelRead := context.WithCancel(workerContext)
		p.reading, p.cancelRead = path, cancelRead
		go func() {
			defer cancelRead()
			result := imagePreviewResult{scope: scope, source: path, generation: generation}
			target := path.Path
			if !filepath.IsAbs(target) {
				target = filepath.Join(scope.Workspace, target)
			}
			// Live file links follow symlinks; the retained-file opener still
			// validates the resolved descriptor and rejects special files.
			if path.Dialog != 0 && filepath.IsAbs(target) && readContext.Err() == nil {
				target, _ = filepath.EvalSymlinks(target)
			}
			if !strings.Contains(path.Path, "://") && !strings.HasPrefix(path.Path, "data:") && filepath.IsAbs(target) && readContext.Err() == nil {
				if file, err := persistence.OpenRetained(target, terminalui.ImageInputLimit); err == nil {
					stop := context.AfterFunc(readContext, func() { _ = file.Close() })
					result.preview = terminalui.DecodePreview(file)
					file.Close()
					stop()
					if result.preview != nil && protocol == terminalui.KittyFileImages {
						if err := result.preview.PrepareFile(); err != nil {
							result.preview.Close()
							result.preview = nil
						}
					}
				}
			}
			if readContext.Err() != nil {
				result.preview.Close()
				result.preview = nil
			}
			select {
			case results <- result:
			case <-workerContext.Done():
				result.preview.Close()
			}
		}()
		return
	}
}

func (p *imagePreviews) finish(result imagePreviewResult) {
	p.pending = false
	p.cancelRead, p.reading = nil, terminalui.ImageSource{}
	if result.scope != p.scope || result.generation != p.generation || result.source.Dialog != 0 && result.source != p.dialog {
		result.preview.Close()
		return
	}
	p.images[result.source] = result.preview
	p.order = append(p.order, result.source)
}
