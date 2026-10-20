package history

import (
	"fmt"
)

const DefaultStorageBytes int64 = 4 << 30

// Limits supplies invocation configuration. Record-kind defaults stay with the
// contributing owner; an explicit override, including zero, takes precedence.
type Limits struct {
	Total int64
	Kinds map[string]int64
}

type CapacityError struct {
	Resource        string
	Required, Limit int64
	Action          string
}

func (e *CapacityError) Error() string {
	return fmt.Sprintf("Mekugi %s needs %d bytes; limit is %d bytes. %s", e.Resource, e.Required, e.Limit, e.Action)
}

func capacityError(resource string, required, limit int64, action string) error {
	return &CapacityError{resource, required, limit, action}
}

// PressureError identifies retryable admission failures, not I/O or record-size
// failures. The completed producer must not be rerun while maintenance works.
type PressureError struct {
	error
	Name string
	Used int64
}

func (e *PressureError) Unwrap() error { return e.error }

func (s *Store) Usage(limits Limits, files map[string]int64, replacement string, size int64) (int64, int64) {
	limit := limits.Total
	if limit <= 0 {
		limit = DefaultStorageBytes
	}
	kind, _, _ := s.service.Kind(replacement)
	kindLimit := kind.Limit
	if override, ok := limits.Kinds[kind.ID]; ok {
		kindLimit = override
	}
	if kind.SeparateBudget {
		limit = kindLimit
	}
	total, own := size, size
	for name, bytes := range files {
		if name == replacement {
			continue
		}
		other, _, _ := s.service.Kind(name)
		if !kind.SeparateBudget && !other.SeparateBudget || kind.SeparateBudget && kind.ID == other.ID {
			total += bytes
		}
		if kind.ID != "" && other.ID == kind.ID {
			own += bytes
		}
	}
	if kind.MaxRecordBytes > 0 && size > kind.MaxRecordBytes {
		return size, kind.MaxRecordBytes
	}
	if !kind.SeparateBudget && kindLimit > 0 && own > kindLimit {
		return own, kindLimit
	}
	return total, limit
}

// Admit checks capacity under serialization and only requests reclamation.
func (tx *Transaction) Admit(limits Limits, replacement string, size int64) error {
	files, err := tx.FileSizes()
	if err != nil {
		return err
	}
	kind, _, err := tx.store.service.Kind(replacement)
	if replacement != "" && !CatalogName(replacement) && err != nil {
		return err
	}
	required, limit := tx.store.Usage(limits, files, replacement, size)
	if required <= limit {
		return nil
	}
	if size > limit && !kind.Keep {
		return capacityError("session storage write", size, limit, "A single record cannot fit; reduce the retained output or split the operation.")
	}
	request := storagePressureRequest{Name: replacement, Growth: max(0, size-files[replacement])}
	if err := tx.requestStoragePrune(request); err != nil {
		return err
	}
	used, _ := tx.store.Usage(limits, files, replacement, files[replacement])
	return &PressureError{
		error: capacityError("retained session storage", required, limit,
			"Background cleanup was requested. Retry retaining this evidence after maintenance frees space; do not rerun the host operation. Running sessions and shared history were preserved."),
		Name: replacement, Used: used,
	}
}
