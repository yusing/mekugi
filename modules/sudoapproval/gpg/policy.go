// Package gpg owns GPG secret framing, independently of sudo authentication.
package gpg

import "strings"

// ValidPassphrase accepts one command-fd response without framing characters.
func ValidPassphrase(value string) bool { return !strings.ContainsAny(value, "\r\n\x00") }
