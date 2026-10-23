package history

import (
	"fmt"

	"github.com/yusing/mekugi/contracts/recordkind"
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
	usage := s.storageUsage(files, replacement)
	usage.total += size - files[replacement]
	usage.own += size - files[replacement]
	return usage.required(limits, size)
}

// storageUsage accounts for one admission target. Maintenance retains it only
// for its revision-validated plan and updates it after each committed effect.
type storageUsage struct {
	kind       recordkind.Spec
	total, own int64
}

func (s *Store) storageUsage(files map[string]int64, replacement string) storageUsage {
	kind, _, _ := s.service.Kind(replacement)
	usage := storageUsage{kind: kind}
	for name, size := range files {
		other, _, _ := s.service.Kind(name)
		usage.add(other, size)
	}
	return usage
}

func (u *storageUsage) add(kind recordkind.Spec, delta int64) {
	if !u.kind.SeparateBudget && !kind.SeparateBudget || u.kind.SeparateBudget && u.kind.ID == kind.ID {
		u.total += delta
	}
	if u.kind.ID != "" && u.kind.ID == kind.ID {
		u.own += delta
	}
}

func (u storageUsage) required(limits Limits, size int64) (int64, int64) {
	limit := limits.Total
	if limit <= 0 {
		limit = DefaultStorageBytes
	}
	kind := u.kind
	kindLimit := kind.Limit
	if override, ok := limits.Kinds[kind.ID]; ok {
		kindLimit = override
	}
	if kind.SeparateBudget {
		limit = kindLimit
	}
	if kind.MaxRecordBytes > 0 && size > kind.MaxRecordBytes {
		return size, kind.MaxRecordBytes
	}
	if !kind.SeparateBudget && kindLimit > 0 && u.own > kindLimit {
		return u.own, kindLimit
	}
	return u.total, limit
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
	usage := tx.store.files.usage(kind)
	usage.total += size - files[replacement]
	usage.own += size - files[replacement]
	required, limit := usage.required(limits, size)
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
	used, _ := tx.store.files.usage(kind).required(limits, files[replacement])
	return &PressureError{
		error: capacityError("retained session storage", required, limit,
			"Background cleanup was requested. Retry retaining this evidence after maintenance frees space; do not rerun the host operation. Running sessions and shared history were preserved."),
		Name: replacement, Used: used,
	}
}
