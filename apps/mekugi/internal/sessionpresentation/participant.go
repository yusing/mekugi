package sessionpresentation

// WorkStatus is a display projection of native work, never a Go work owner.
type WorkStatus string

const (
	Pending     WorkStatus = "pending"
	WorkRunning WorkStatus = "working"
	Waiting     WorkStatus = "waiting"
	Paused      WorkStatus = "paused"
	Completed   WorkStatus = "completed"
	Failed      WorkStatus = "failed"
	Interrupted WorkStatus = "interrupted"
	Uncertain   WorkStatus = "uncertain"
)

// Participant is detached roster identity, without instructions or dispatch state.
type Participant struct {
	Thread, Parent, Task, Role string
	Status                     WorkStatus
	NonAttachable              bool
}
