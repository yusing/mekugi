package recovery

import "github.com/yusing/mekugi/modules/outputrecovery/internal/record"

// MaxOutputBytes bounds combined normalized omitted stdout and stderr, not
// delivered text, encoded records, or aggregate retained storage.
const MaxOutputBytes = record.MaxOutputBytes

// ValidateOutputSize is a pure preflight. Publication still validates payloads,
// canonical record size and storage quota before exposing any reference.
func ValidateOutputSize(size int64) error { return record.ValidateOutputSize(size) }
