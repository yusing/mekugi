package command

import "fmt"

// Spec is an executable contribution, independent of the backend's wire format.
type Spec struct {
	Name string
}

func (s Spec) Validate() error { return ValidateName(s.Name) }

// ValidateName accepts portable executable leaf names.
func ValidateName(name string) error {
	if name == "" || name == "." || name == ".." {
		return fmt.Errorf("%q: invalid executable name", name)
	}
	for _, c := range name {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' {
			continue
		}
		return fmt.Errorf("%q: invalid executable name", name)
	}
	return nil
}
