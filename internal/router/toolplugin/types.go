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

	Carrier struct {
		Kind        string                     `json:"kind"`
		Name        string                     `json:"name"`
		Payload     string                     `json:"payload"`
		Template    string                     `json:"template"`
		Params      map[string]json.RawMessage `json:"params"`
		RetainInput *bool                      `json:"retainInput"`
	}

	Translation struct {
		Rejected   bool     `json:"rejected"`
		Diagnostic string   `json:"diagnostic"`
		Arguments  []string `json:"arguments"`
		Carrier    Carrier  `json:"carrier"`
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
