package feed

// Source: internal/ui/activity/blocks.go:18:83@[3c06e4fa] semantic content.
// Content is renderer-neutral text and target data. Host adapters also use it
// for authored messages and reasoning; it never establishes execution state.
type Content struct {
	Kind       string   // op, reads, message, start, error, text
	Verb       string   // Operation verb, or a message headline.
	Label      string   // Markdown remainder of the operation label.
	Timeouts   []string // Explicit execution bounds, as displayed.
	Code       string   // Inline code or fenced program under the label.
	Lang       string
	Fenced     bool
	Body       string
	Reads      []Read
	EditSource string // Editing source shared by this invocation's file rows.
	EditHeader bool   // First row of a contiguous source group.
}

type Read struct {
	Path   string
	Ranges []string
	Lines  int // Collapsed content read from this target, once merged.
}
