package router

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

const (
	composerTreeEntries = 256
	composerTreeScan    = 4096
	composerTreeBytes   = 16 << 10
	composerIgnoreBytes = 64 << 10
)

type composerTreeIgnore struct {
	directory string
	rules     []execIgnoreRule
}

// Directory attachments read names only. Bounds include ignored entries and
// ignore-file input, not just the resulting visible tree.
func composerDirectoryTree(root string) (string, error) {
	// The selected alias remains in attachment framing/display, but ignore
	// ancestry must describe the directory actually being read.
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	root = resolved
	var ancestors []string
	for directory := root; ; directory = filepath.Dir(directory) {
		ancestors = append(ancestors, directory)
		if len(ancestors) > 64 {
			return "", fmt.Errorf("directory ignore ancestry exceeds 64 levels")
		}
		if _, err := os.Lstat(filepath.Join(directory, ".git")); err == nil || filepath.Dir(directory) == directory {
			break
		}
	}
	slices.Reverse(ancestors)
	ignoreBytes := 0
	loadIgnore := func(directory string) (composerTreeIgnore, error) {
		group := composerTreeIgnore{directory: directory}
		data, err := readComposerFile(filepath.Join(directory, ".gitignore"))
		if errors.Is(err, os.ErrNotExist) {
			return group, nil
		}
		if err != nil {
			return group, fmt.Errorf("cannot read directory ignore rules: %w", err)
		}
		ignoreBytes += len(data)
		if ignoreBytes > composerIgnoreBytes {
			return group, fmt.Errorf("directory ignore rules exceed %d-byte limit", composerIgnoreBytes)
		}
		for line := range strings.SplitSeq(string(data), "\n") {
			if rule, ok := parseExecIgnoreLine(line); ok {
				group.rules = append(group.rules, rule)
			}
		}
		return group, nil
	}
	var ignores []composerTreeIgnore
	for _, directory := range ancestors {
		group, err := loadIgnore(directory)
		if err != nil {
			return "", err
		}
		ignores = append(ignores, group)
	}
	var tree strings.Builder
	tree.WriteString("./\n")
	listed, scanned := 0, 0
	var walk func(string, int, []composerTreeIgnore) error
	walk = func(directory string, depth int, ignores []composerTreeIgnore) error {
		f, err := os.Open(directory)
		if err != nil {
			return err
		}
		entries, err := f.ReadDir(composerTreeScan - scanned + 1)
		f.Close()
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		// Never select an arbitrary filesystem-order subset of an oversized
		// directory. Omit that listing whole so truncated output is stable too.
		if len(entries) > composerTreeScan-scanned {
			return fmt.Errorf("scan limit of %d entries", composerTreeScan)
		}
		scanned += len(entries)
		slices.SortFunc(entries, func(a, b os.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
		for _, entry := range entries {
			path := filepath.Join(directory, entry.Name())
			if pickerVCSPath(entry.Name()) || composerTreeIgnored(path, entry.IsDir(), ignores) {
				continue
			}
			name := entry.Name()
			// Quote control characters, invalid UTF-8 and other ambiguous names;
			// a filename cannot forge another tree row or a truncation notice.
			if strconv.Quote(name) != "\""+name+"\"" {
				name = strconv.Quote(name)
			}
			if entry.IsDir() {
				name += "/"
			} else if entry.Type()&os.ModeSymlink != 0 {
				name += " (symlink)"
			}
			line := strings.Repeat("  ", depth-1) + "- " + name + "\n"
			if listed == composerTreeEntries || tree.Len()+len(line) > composerTreeBytes-256 {
				return fmt.Errorf("listing limit of %d entries or %d bytes", composerTreeEntries, composerTreeBytes)
			}
			tree.WriteString(line)
			listed++
			if entry.IsDir() && depth < 2 {
				group, err := loadIgnore(path)
				if err != nil {
					return err
				}
				if err := walk(path, depth+1, append(slices.Clone(ignores), group)); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(root, 1, ignores); err != nil {
		// Keep even path-bearing errors inside the reserved tail, and prevent
		// control characters in filesystem errors from forging tree rows.
		reason := strconv.QuoteToASCII(err.Error())
		if len(reason) > 160 {
			reason = reason[:160] + "...\""
		}
		fmt.Fprintf(&tree, "[TRUNCATED: %s; remaining entries not listed.]\n", reason)
	} else if listed == 0 {
		tree.WriteString("(empty after .gitignore and VCS metadata exclusions)\n")
	}
	return tree.String(), nil
}

func composerTreeIgnored(path string, isDir bool, groups []composerTreeIgnore) bool {
	// An excluded parent cannot be re-included by a descendant negation.
	relative, err := filepath.Rel(groups[0].directory, path)
	if err != nil || !filepath.IsLocal(relative) {
		return false
	}
	parts := strings.Split(filepath.ToSlash(relative), "/")
	for index := range parts {
		prefix := filepath.Join(groups[0].directory, filepath.FromSlash(strings.Join(parts[:index+1], "/")))
		ignored := false
		for _, group := range groups {
			candidate, err := filepath.Rel(group.directory, prefix)
			if err != nil || candidate == "." || !filepath.IsLocal(candidate) {
				continue
			}
			for _, rule := range group.rules {
				if rule.dirOnly && index == len(parts)-1 && !isDir {
					continue
				}
				match := filepath.ToSlash(candidate)
				if rule.basename {
					match = filepath.Base(candidate)
				}
				if rule.pattern.MatchString(match) {
					ignored = !rule.negate
				}
			}
		}
		if ignored {
			return true
		}
	}
	return false
}
