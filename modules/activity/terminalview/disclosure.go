package terminalview

import "github.com/yusing/mekugi/modules/activity/presentation"

// Disclosure retains terminal annotations by stable entry identity and record-local row.
// A replacement rebuilds its rows; reordering records cannot detach annotations.
// It holds no source, content values or retained output snapshots.
type Disclosure map[uint64]map[int]Annotation

func (d *Disclosure) Keep(source uint64, blocks []Block) []presentation.Value {
	values := Values(blocks)
	if *d == nil {
		*d = make(Disclosure)
	}
	delete(*d, source)
	for i, block := range blocks {
		if block.Annotation == (Annotation{}) {
			continue
		}
		if (*d)[source] == nil {
			(*d)[source] = make(map[int]Annotation)
		}
		(*d)[source][i] = block.Annotation
	}
	return values
}
func (d Disclosure) Remove(source uint64) { delete(d, source) }
func (d Disclosure) Blocks(values []presentation.Value) []Block {
	blocks := make([]Block, len(values))
	for i, value := range values {
		blocks[i] = Block{Value: value, Annotation: d[value.Source][i]}
	}
	return blocks
}
func (d *Disclosure) Collapse(source uint64, row int) {
	if *d == nil {
		*d = make(Disclosure)
	}
	if (*d)[source] == nil {
		(*d)[source] = make(map[int]Annotation)
	}
	annotation := (*d)[source][row]
	annotation.Collapsed = true
	(*d)[source][row] = annotation
}

// Values strips terminal annotations before publishing parsed observations.
func Values(blocks []Block) []presentation.Value {
	values := make([]presentation.Value, len(blocks))
	for i, block := range blocks {
		values[i] = block.Value
	}
	return values
}
