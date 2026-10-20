// Package commandanalysis owns deterministic command and executable facts.
package commandanalysis

// Source: internal/vcsguard/vcsguard.go:39:46@[465a8ff3] Tools and IsTool

import (
	"path/filepath"
	"slices"
)

// VCSTools names the version-control and hosting executables recognized by Mekugi.
var VCSTools = []string{"git", "gh", "hg", "svn", "jj"}

// IsVCSTool identifies an executable without deciding its effects or approval.
func IsVCSTool(name string) bool {
	return slices.Contains(VCSTools, filepath.Base(name))
}
