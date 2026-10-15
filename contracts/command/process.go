package command

// ProcessScope states the caller's ownership of subprocess groups and descendants.
type ProcessScope uint8

const (
	// StandaloneProcess permits a command to own its resolver process groups.
	StandaloneProcess ProcessScope = iota
	// HostProcessGroup keeps descendants in the host's group without process-wide cleanup.
	HostProcessGroup
	// DedicatedHostProcess additionally permits cleanup of this worker's descendants.
	// Only a dedicated authenticated frontend worker may receive this scope.
	DedicatedHostProcess
)
