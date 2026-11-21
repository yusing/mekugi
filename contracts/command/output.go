package command

type Output struct {
	FailureClass string `json:"failureClass,omitempty"`
	Stdout       string `json:"stdout"`
	Stderr       string `json:"stderr"`
	ExitCode     int    `json:"exitCode"`
}
