package vcsguard

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yusing/mekugi/internal/shellsyntax"
)

// RealEnvironment names the variable a path function sets for the guard: the
// absolute path the shell was asked to run. The guard runs that file rather
// than the next PATH match, and removes the variable before it does.
const RealEnvironment = "MEKUGI_VCS_GUARD_REAL"

// UserZdotdirEnvironment keeps the ZDOTDIR the session started with, unset
// when it had none, while ZDOTDIR points at the zsh startup wrappers.
const UserZdotdirEnvironment = "MEKUGI_USER_ZDOTDIR"

// standardDirectories are where packages install the guarded tools, so a path
// to one of them is guarded even when it is not on PATH.
var standardDirectories = []string{
	"/usr/local/bin", "/usr/bin", "/bin", "/usr/local/sbin", "/usr/sbin", "/sbin",
	"/opt/homebrew/bin", "/opt/local/bin", "/home/linuxbrew/.linuxbrew/bin", "/snap/bin",
}

// KnownPaths lists absolute paths of the guarded tools found in pathList and
// the standard directories, with the files their symlinks resolve to.
func KnownPaths(pathList string) []string {
	var paths []string
	for _, directory := range slices.Concat(filepath.SplitList(pathList), standardDirectories) {
		if !filepath.IsAbs(directory) || filepath.Base(directory) == Directory {
			continue
		}
		for _, tool := range Tools {
			path := filepath.Join(directory, tool)
			if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
				continue
			}
			paths = append(paths, path)
			if resolved, err := filepath.EvalSymlinks(path); err == nil && slices.Contains(Tools, filepath.Base(resolved)) {
				paths = append(paths, resolved)
			}
		}
	}
	slices.Sort(paths)
	return slices.Compact(paths)
}

// Functions returns Bash or zsh code that defines a function for each
// absolute path to a guarded tool: known, and each guarded name in every
// absolute PATH entry when the code runs. A command that names such a path
// runs the guard, which sees the same arguments and runs that file. Both
// shells look up functions before running a name that contains a slash; a
// path the shell does not run itself, as through env, exec or command,
// bypasses its function.
func Functions(shell, guardDirectory string, known []string) string {
	var list strings.Builder
	for _, path := range known {
		list.WriteString(" " + shellsyntax.Quote(path))
	}
	// The value is shell source for eval, carried as data through the outer
	// expansion so runtime-directory characters cannot become shell code.
	guard := shellsyntax.Quote(shellsyntax.Quote(guardDirectory))
	tools := strings.Join(Tools, " ")
	// Function names cannot be quoted, so paths outside a plain character set
	// are left unguarded rather than defined unsafely.
	define := `eval "$__mekugi_p() { ` + RealEnvironment + `=$__mekugi_p $__mekugi_guard/${__mekugi_p##*/} \"\$@\"; }" 2>/dev/null`
	if shell == "zsh" {
		return `() {
  emulate -L zsh
  local __mekugi_d __mekugi_t __mekugi_p
  local __mekugi_guard=` + guard + `
  local -a __mekugi_ps
  __mekugi_ps=(` + list.String() + ` )
  for __mekugi_d in $path; do
    [[ $__mekugi_d == /* && ${__mekugi_d:t} != ` + Directory + ` ]] || continue
    for __mekugi_t in ` + tools + `; do __mekugi_ps+=($__mekugi_d/$__mekugi_t); done
  done
  for __mekugi_p in $__mekugi_ps; do
    [[ $__mekugi_p == *[^A-Za-z0-9_./+@-]* || ! -f $__mekugi_p || ! -x $__mekugi_p ]] && continue
    ` + define + `
  done
}
`
	}
	return `__mekugi_vcs_paths() {
  local IFS=: __mekugi_d __mekugi_t __mekugi_p
  local __mekugi_guard=` + guard + `
  local -a __mekugi_ds __mekugi_ps
  __mekugi_ps=(` + list.String() + ` )
  read -r -a __mekugi_ds <<<"$PATH"
  for __mekugi_d in "${__mekugi_ds[@]}"; do
    case $__mekugi_d in /*/` + Directory + `|/` + Directory + `) continue ;; /*) ;; *) continue ;; esac
    for __mekugi_t in ` + tools + `; do __mekugi_ps+=("$__mekugi_d/$__mekugi_t"); done
  done
  for __mekugi_p in "${__mekugi_ps[@]}"; do
    case $__mekugi_p in *[!A-Za-z0-9_./+@-]*) continue ;; esac
    [ -f "$__mekugi_p" ] && [ -x "$__mekugi_p" ] || continue
    ` + define + `
  done
}
# POSIX mode rejects these names, and an eval error there ends the shell.
case :$SHELLOPTS: in *:posix:*) ;; *) __mekugi_vcs_paths ;; esac
unset -f __mekugi_vcs_paths
`
}

// ZshPath returns zsh code that moves entries to the front of PATH. Each zsh
// startup file runs it again, after the user's file of that name, which may
// have put other directories first.
func ZshPath(entries ...string) string {
	var list strings.Builder
	for _, entry := range entries {
		list.WriteString(" " + shellsyntax.Quote(entry))
	}
	return `() {
  emulate -L zsh
  local __mekugi_e
  for __mekugi_e in` + list.String() + `; do path=(${path:#$__mekugi_e}); done
  path=(` + list.String() + ` $path)
}
export PATH
`
}

// zshSetup names the setup file in a directory WriteZshStartup wrote.
const zshSetup = "mekugi-setup.zsh"

// IsZshStartup reports whether directory holds startup files that
// WriteZshStartup wrote, as an outer session's ZDOTDIR does.
func IsZshStartup(directory string) bool {
	_, err := os.Stat(filepath.Join(directory, zshSetup))
	return err == nil
}

// zshStartupFiles are the files zsh reads from ZDOTDIR, in order.
var zshStartupFiles = []string{".zshenv", ".zprofile", ".zshrc", ".zlogin"}

// WriteZshStartup writes zsh startup files to directory, which ZDOTDIR then
// names. Each runs the user's own file of the same name, from the ZDOTDIR the
// session started with or a later one the user's files set, then sources
// setup. Setup thus runs after every startup file that may replace PATH,
// for login and plain `zsh -c` shells alike.
func WriteZshStartup(directory, setup string) error {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(directory, zshSetup), []byte(setup), 0o600); err != nil {
		return err
	}
	ours := shellsyntax.Quote(directory)
	for i, name := range zshStartupFiles {
		var file strings.Builder
		if i == 0 {
			file.WriteString(`if (( ${+` + UserZdotdirEnvironment + `} )); then typeset -g __mekugi_zdotdir=$` + UserZdotdirEnvironment + `; else unset __mekugi_zdotdir; fi
`)
		}
		file.WriteString(`if (( ${+__mekugi_zdotdir} )); then ZDOTDIR=$__mekugi_zdotdir; else unset ZDOTDIR; fi
if [[ -r ${ZDOTDIR:-$HOME}/` + name + ` ]]; then source ${ZDOTDIR:-$HOME}/` + name + `; fi
if (( ${+ZDOTDIR} )); then typeset -g __mekugi_zdotdir=$ZDOTDIR; else unset __mekugi_zdotdir; fi
ZDOTDIR=` + ours + `; export ZDOTDIR
source ` + ours + `/` + zshSetup + `
`)
		if err := os.WriteFile(filepath.Join(directory, name), []byte(file.String()), 0o600); err != nil {
			return err
		}
	}
	// Login shells read .zlogout from the current ZDOTDIR. Restore the
	// user's directory for their cleanup instead of hiding it behind ours.
	return os.WriteFile(filepath.Join(directory, ".zlogout"), []byte(`if (( ${+__mekugi_zdotdir} )); then ZDOTDIR=$__mekugi_zdotdir; else unset ZDOTDIR; fi
if [[ -r ${ZDOTDIR:-$HOME}/.zlogout ]]; then source ${ZDOTDIR:-$HOME}/.zlogout; fi
`), 0o600)
}
