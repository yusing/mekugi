package changes

import "github.com/yusing/mekugi/libraries/diffmodel"

// FrozenRead selects immutable retained records and exact chunk bounds. It owns
// no capture bytes; projection reads the selected evidence through its owner.
type FrozenRead struct {
	Workspace  string
	Captures   []FrozenCapture
	Fallback   string
	Diagnostic string
}

type FrozenCapture struct {
	ChangeID, Workspace, Correlation, CallID, Caller string
	Order                                            uint64
	FirstChunk, LastChunk                            uint64
}

// LegacyFrozenRead is the read-only version-1 completion format. New selections
// never populate it; old sessions may still reference these retained bytes.
type LegacyFrozenRead struct {
	Workspace  string
	Captures   []LegacyFrozenCapture
	Fallback   string
	Diagnostic string
}

type LegacyFrozenCapture struct {
	ChangeID, Workspace, Coverage string
	Order                         uint64
	Files                         []LegacyFrozenFile
}

type LegacyFrozenFile struct {
	Key   string
	Chunk uint64
	File  diffmodel.ReviewFile
}
