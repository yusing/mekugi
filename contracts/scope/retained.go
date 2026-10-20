package scope

// Retained identifies the host-selected durable session. Values identify scope;
// authentication and the invoking caller's authority remain with the host.
type Retained struct {
	Directory string
	Namespace string
	Thread    string
}
