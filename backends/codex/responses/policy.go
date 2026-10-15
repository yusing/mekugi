// Source: internal/providercodec/policy.go:1:36@[48bc03a0] protocol implementation.
package responses

// Policy supplies capabilities resolved by the authenticated provider boundary.
// The codec does not select providers, inspect credentials, or retain sessions.
type Policy struct {
	Diagnostics                 Diagnostics
	CompletionIncludesReasoning bool
	ReplaySummaries             bool
	CloseFailedStream           bool
	NoHostedSearchInstruction   string
	CustomInputInstruction      string
	ExecInstruction             string
	FormatInstructionPrefix     string
	RestoreReasoning            func(string) (map[string]any, error)
	SealReasoning               func(any) string
	ResolveEffort               func(string) (string, error)
}

// Diagnostics keeps error construction and transport classification with the
// caller. DescribeError supplies the code and caller-facing terminal message.
type Diagnostics struct {
	Name, Subject, Prefix, ContentErrorCode string
	BufferBytes                             int
	Error                                   func(code, summary, detail string) error
	DescribeError                           func(error) (code, message string, ok bool)
	Incompatible                            func(code, message string) error
	ForwardError                            func(error) error
}

func (d Diagnostics) streamError(code, message string) error {
	return d.Error(d.Prefix+"_stream_"+code, message, "")
}

func (d Diagnostics) contentError(message string) error {
	return d.Error(d.ContentErrorCode, message, "")
}
