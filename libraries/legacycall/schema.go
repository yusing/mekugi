package legacycall

import (
	jsonv1 "encoding/json"
	"time"
)

// Source: internal/router/mekugi_store.go:43:51@[ff834f05] replayRecord
type Record struct {
	Version      int
	Workspace    string
	CallID       string
	Commentary   bool
	CaptureOrder uint64 `json:",omitzero"`
	History      History
	Snapshots    *Snapshots `json:",omitempty"`
}

// Source: internal/router/mekugi_history.go:26:67@[ff834f05] mekugiHistory
type History struct {
	ToolName             string
	PluginID             string
	Script               string
	Root                 string
	ExecutingThread      string `json:",omitempty"`
	Caller               string `json:",omitempty"`
	Source               string `json:",omitempty"`
	ChangeID             string
	ReviewFiles          []ReviewFile
	NativePatches        []NativePatch `json:",omitempty"`
	ResolvedBaseline     *Baseline     `json:",omitempty"`
	ExecObservation      *Observation  `json:",omitempty"`
	ExecOutcome          *Outcome      `json:",omitempty"`
	HostResults          []HostResult  `json:",omitempty"`
	CommandSegments      *Segments     `json:",omitempty"`
	CarrierName          string
	CarrierKind          string
	CarrierPayload       string
	Report               string
	OutputWarning        string
	TranslationError     string
	CorrelationID        string
	Attempt              int
	UpstreamItem         map[string]jsonv1.RawMessage
	ReplayCarrier        bool
	CommentaryMessageIDs []string
	AlreadySatisfied     bool
}

// Source: review.go:14:34@[ff834f05] ReviewFile
type ReviewFile struct {
	BeforePath string
	AfterPath  string
	Diff       string
	Incomplete string `json:",omitzero"`
	Directory  bool   `json:",omitzero"`
	Binary     bool   `json:",omitzero"`
	Link       bool   `json:",omitzero"`
	CopyFrom   string `json:",omitzero"`
	Origin     string `json:",omitzero"`
	OriginNote string `json:",omitzero"`
}

// Source: internal/router/native_apply_patch.go:27:30@[ff834f05] nativePatchObservation
type NativePatch struct {
	Input string
	Files []NativeFile
}

// Source: internal/router/native_apply_patch.go:32:41@[ff834f05] nativePatchFileSnapshot
type NativeFile struct {
	BeforePath   string `json:",omitempty"`
	AfterPath    string `json:",omitempty"`
	Before       string `json:",omitempty"`
	Exists       bool   `json:",omitzero"`
	Error        string `json:",omitempty"`
	TargetBefore string `json:",omitempty"`
	TargetExists bool   `json:",omitzero"`
	TargetError  string `json:",omitempty"`
}

// Source: internal/router/resolved_stock_edits.go:11:17@[ff834f05] resolvedStockBaseline
type Baseline struct {
	Root      string
	Shell     string
	Files     []File     `json:",omitempty"`
	Omitted   []Omission `json:",omitempty"`
	Inventory *Inventory `json:",omitempty"`
}

// Source: internal/router/exec_observation.go:55:59@[ff834f05] execCommandInput
type Command struct {
	Command string
	Workdir string
	Shell   string `json:",omitempty"`
}

// Source: internal/router/exec_observation.go:79:99@[ff834f05] execFileSnapshot
type File struct {
	Origin      string `json:",omitempty"`
	AlsoManaged string `json:",omitempty"`
	Path        string
	Kind        string `json:",omitempty"`
	Content     string `json:",omitempty"`
	Size        int64  `json:",omitzero"`
	Hash        string `json:",omitempty"`
	Link        string `json:",omitempty"`
	Stamp       string `json:",omitempty"`
	Error       string `json:",omitempty"`
	CopyOf      string `json:",omitempty"`
}

// Source: internal/router/exec_observation.go:102:109@[ff834f05] execOmission
type Omission struct {
	Origin   string `json:",omitempty"`
	Path     string
	Reason   string
	Deferred bool `json:",omitzero"`
}

// Source: internal/router/exec_observation.go:113:145@[ff834f05] execObservation
type Observation struct {
	Commands       []Command
	CommandClasses []string `json:",omitempty"`
	Class          string
	Labels         []string `json:",omitempty"`
	Reason         string   `json:",omitempty"`
	Files          []File
	Omitted        []Omission `json:",omitempty"`
	Listings       []Listing  `json:",omitempty"`
	CodeMode       bool       `json:",omitzero"`
	Roots          []string   `json:",omitempty"`
	WindowStart    time.Time  `json:",omitzero"`
	Programs       []Program  `json:",omitempty"`
	Group          string     `json:",omitempty"`
	Excluded       []string   `json:",omitempty"`
	RepeatedPaths  bool       `json:",omitzero"`
	LiteralClass   string     `json:",omitempty"`
	Inventory      *Inventory `json:",omitempty"`
	Tree           string     `json:",omitempty"`
}

// Source: internal/router/exec_observation.go:164:186@[ff834f05] execOutcome
type Outcome struct {
	DeletedDirectories []string `json:",omitempty"`
	OutputRef          string   `json:",omitempty"`
	Status             string
	Exit               *int `json:",omitempty"`
	Class              string
	Labels             []string `json:",omitempty"`
	Coverage           string
	CodeMode           bool     `json:",omitzero"`
	Scope              []string `json:",omitempty"`
	ScopeReason        string   `json:",omitempty"`
	Unswept            string   `json:",omitempty"`
	Overlaps           []string `json:",omitempty"`
	Background         []string `json:",omitempty"`
	SharedWith         string   `json:",omitempty"`
}

// Source: internal/router/exec_observation.go:505:510@[ff834f05] execListing
type Listing struct {
	Origin  string `json:",omitempty"`
	Root    string
	Entries map[string]string `json:",omitempty"`
}

// Source: internal/router/exec_inventory.go:16:36@[ff834f05] execInventory
type Inventory struct {
	Version      int                       `json:",omitzero"`
	Directories  map[string]DirectoryStamp `json:",omitempty"`
	Root         string
	Entries      map[string]string `json:",omitempty"`
	Blobs        map[string]string `json:",omitempty"`
	Files        []File            `json:",omitempty"`
	Repositories []string          `json:",omitempty"`
	Pruned       []string          `json:",omitempty"`
	Omitted      []Omission        `json:",omitempty"`
}

// Source: internal/router/exec_inventory.go:9:14@[ff834f05] execDirectoryStamp
type DirectoryStamp struct {
	Exists   bool
	Stamp    string
	Digest   string
	Complete bool
}

// Source: internal/execscope/shell.go:97:105@[ff834f05] Program
type Program struct {
	Label  string
	Direct bool `json:",omitzero"`
	VCS    bool `json:",omitzero"`
}

// Source: internal/router/native_tool_trace.go:50:55@[ff834f05] nativeToolResult
type HostResult struct {
	CallID   string
	Tool     string
	Status   string
	ExitCode *int `json:",omitempty"`
}

// Source: internal/router/command_segments.go:19:24@[ff834f05] retainedCommandSegments
type Segments struct {
	Command string
	Exit    int
	Output  [32]byte
	Parts   []Segment
}

// Source: internal/router/command_segments.go:26:32@[ff834f05] retainedCommandSegment
type Segment struct {
	Timing  Timing `json:",omitzero"`
	Source  string
	Skipped bool
	Exit    int
	Output  *string
}

// Source: internal/execsegment/report.go:12:16@[ff834f05] Timing
type Timing struct {
	Started   time.Time `json:"started,omitzero"`
	Ended     time.Time `json:"ended,omitzero"`
	ElapsedNS int64     `json:"elapsed_ns,omitzero"`
}
