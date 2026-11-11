package responses

// Diagnostics keeps transport error construction with the caller.
type Diagnostics struct {
	Error func(code, summary, detail string) error
}
