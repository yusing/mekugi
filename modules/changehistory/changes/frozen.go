package changes

import "github.com/yusing/mekugi/libraries/diffmodel"

// FrozenRead retains decoded capture bytes, not pointers to mutable call records.
// Automatic final projections can omit acknowledged chunks without reacquisition.
type FrozenRead struct {
	Workspace  string
	Captures   []FrozenCapture
	Fallback   string
	Diagnostic string
}

type FrozenCapture struct {
	ChangeID, Workspace, Coverage string
	Order                         uint64
	Files                         []FrozenFile
}

type FrozenFile struct {
	Key   string
	Chunk uint64
	File  diffmodel.ReviewFile
}
