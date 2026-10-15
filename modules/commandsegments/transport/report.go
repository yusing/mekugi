package transport

// Source: internal/execsegment/hook.go:169:214@[465a8ff3] report protocol

import "time"

// Protocol is the helper-to-router message version.
const Protocol = 4

// Timing records helper-observed command boundaries. ElapsedNS is measured
// with the helper's monotonic clock, independently of wall-clock adjustments.
type Timing struct {
	Started   time.Time `json:"started,omitzero"`
	Ended     time.Time `json:"ended,omitzero"`
	ElapsedNS int64     `json:"elapsed_ns,omitzero"`
}

// Message is one line of the helper's report. Hello opens a report and the
// router answers with Reply; the rest follow in the order the shell and its
// output produced them.
type Message struct {
	Timing   Timing   `json:"timing,omitzero"`
	Type     string   `json:"t"`
	Version  int      `json:"v,omitzero"`
	Thread   string   `json:"thread,omitempty"`
	Script   string   `json:"script,omitempty"`
	Segments []string `json:"segments,omitempty"`
	Terminal bool     `json:"tty,omitzero"`
	Tree     bool     `json:"tree,omitzero"`
	Index    int      `json:"i,omitzero"`
	Data     string   `json:"d,omitempty"`
	Code     *int     `json:"c,omitempty"`
}

// Message types.
const (
	Hello   = "hello"
	Begin   = "b"
	Output  = "o"
	Created = "c"
	End     = "e"
	Done    = "d"
	// Closed follows all output and ends the report without waiting for FIFO EOF.
	Closed = "closed"
	// Lossy reports that the helper stopped reporting output to stay within
	// its bound; statuses continue.
	Lossy = "lossy"
)

// Reply accepts or declines a hello.
type Reply struct {
	OK bool `json:"ok"`
}
