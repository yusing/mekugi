package capture

// Service supplies transport evidence operations for a composed invocation.
// Open returns a recorder with an explicit caller-owned lifetime; offline reads
// do not allocate recorder state or acquire output resources.
type Service struct{}
