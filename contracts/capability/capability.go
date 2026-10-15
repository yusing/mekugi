package capability

type Mechanism string

const (
	ExecutableFrontend Mechanism = "executable-frontend"
	InputProjection    Mechanism = "input-projection"
)

type State uint8

const (
	Unknown State = iota
	Available
	Unsupported
)

type Result struct {
	State  State
	Reason string
}

type Requirement struct {
	Mechanism Mechanism
	Required  bool
}
