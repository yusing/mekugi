package recordkind

import "strings"

// HashedName recognizes prefix + 64 lowercase hexadecimal digits + suffix.
func HashedName(prefix, suffix string) func(string) bool {
	return func(name string) bool {
		hash, ok := strings.CutPrefix(name, prefix)
		if !ok {
			return false
		}
		hash, ok = strings.CutSuffix(hash, suffix)
		return ok && len(hash) == 64 && strings.Trim(hash, "0123456789abcdef") == ""
	}
}
