package toolplugin

import "encoding/json"

type (
	Tool struct {
		Specification  json.RawMessage `json:"specification"`
		NativeExecutor string          `json:"nativeExecutor,omitempty"`
	}

	OmittedOutput struct {
		// Host-owned mcat provenance, separate from serialized output.
		SourceRow  uint64 `json:"-"`
		MaxTokens  int    `json:"-"`
		StdoutKind string `json:"stdoutKind,omitempty"`
		StderrKind string `json:"stderrKind,omitempty"`
		Stdout     string `json:"stdout"`
		Stderr     string `json:"stderr"`
	}

	ExecutionOutput struct {
		OmittedOutput *OmittedOutput `json:"omittedOutput,omitempty"`
		FailureClass  string         `json:"failureClass,omitempty"`
		Stdout        string         `json:"stdout"`
		Stderr        string         `json:"stderr"`
		ExitCode      int            `json:"exitCode"`
	}
)
