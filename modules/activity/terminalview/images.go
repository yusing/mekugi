package terminalview

import "github.com/yusing/mekugi/libraries/terminalui"

type feedImage struct {
	source    terminalui.ImageSource
	row       int
	placement terminalui.ImagePlacement
}

func (v *View) appendImages(feed *Feed, first, last, head, width int) {
	if v.ImageProtocol == terminalui.NoImages {
		return
	}
	for _, entry := range v.entries[first : last+1] {
		if !v.Visible(entry) || entry.Kind != "tool" || len(entry.Values) != 1 {
			continue
		}
		verb := entry.Values[0].Verb
		if verb != "Generated" && (verb != "View" || v.Expansion == 0) {
			continue
		}
		for _, path := range entry.Images {
			source := terminalui.ImageSource{Path: path, Entry: entry.Seq, Revision: entry.Revision}
			preview := v.ImagePreviews[source]
			columns, rows := preview.Size(width, v.ImageProtocol, v.ImageCellWidth, v.ImageCellHeight)
			if rows == 0 {
				if _, known := v.ImagePreviews[source]; !known {
					feed.images = append(feed.images, feedImage{source: source, row: head})
				}
				continue
			}
			row := len(feed.Lines)
			feed.images = append(feed.images, feedImage{source: source, row: row, placement: terminalui.ImagePlacement{Image: preview, Columns: columns, Rows: rows, TotalRows: rows}})
			for range rows {
				feed.Lines = append(feed.Lines, "")
				feed.Heads = append(feed.Heads, head)
				feed.Snippets = append(feed.Snippets, Snippet{})
				feed.Questions = append(feed.Questions, 0)
			}
		}
	}
}

func (v *View) imageViewport(feed Feed, rows int) {
	v.ImageDemands, v.ImagePlacements = nil, nil
	for _, image := range feed.images {
		p := image.placement
		if image.row+max(1, p.Rows) <= v.Offset || image.row >= v.Offset+rows {
			continue
		}
		v.ImageDemands = append(v.ImageDemands, image.source)
		if p.Image == nil {
			continue
		}
		p.First = max(0, v.Offset-image.row)
		p.Y = max(0, image.row-v.Offset)
		p.Rows = min(p.Rows-p.First, rows-p.Y)
		v.ImagePlacements = append(v.ImagePlacements, p)
	}
}
