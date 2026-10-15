package command

type (
	OmittedOutput struct {
		// Source provenance and requested recovery budget, separate from serialized output.
		SourceRow  uint64 `json:"-"`
		MaxTokens  int    `json:"-"`
		StdoutKind string `json:"stdoutKind,omitempty"`
		StderrKind string `json:"stderrKind,omitempty"`
		Stdout     string `json:"stdout"`
		Stderr     string `json:"stderr"`
	}

	Output struct {
		Delivery      PendingDelivery `json:"-"`
		OmittedOutput *OmittedOutput  `json:"omittedOutput,omitempty"`
		FailureClass  string          `json:"failureClass,omitempty"`
		Stdout        string          `json:"stdout"`
		Stderr        string          `json:"stderr"`
		ExitCode      int             `json:"exitCode"`
	}
)
