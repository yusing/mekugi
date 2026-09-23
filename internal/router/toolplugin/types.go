package toolplugin

import "encoding/json"

type (
	Tool struct {
		Specification json.RawMessage `json:"specification"`
	}

	Plugin struct {
		ID     string `json:"id"`
		Module string `json:"module"`
		Tools  []Tool `json:"tools"`
	}

	OmittedOutput struct {
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

	Snapshot struct {
		Root           string
		NodeExecutable string
		Plugins        []Plugin
		Diagnostics    []string
	}
)
