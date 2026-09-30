package execsegment

import (
	"time"

	"github.com/yusing/mekugi/internal/shellsyntax"
)

// Guard marks a shell that already decided whether to track its command, so
// nested Bash commands started by that command are never tracked.
const Guard = "MEKUGI_EXEC_TRACK"

// Hook is Bash startup source, read from BASH_ENV, that tracks the command
// script Codex passed with -c. It decides before any segment runs: only when
// the helper plans the script and the router matches it to a live Codex
// command does the shell run the rewrite in place of the script and exit.
// Otherwise it returns and Bash runs the original script itself, so the
// command never runs twice.
//
// Codex's own snapshot scripts, including the wrapper that re-executes the
// command shell, pass through without setting the guard: the snapshot would
// otherwise record it for every later command. Scripts without a list
// operator or newline are single commands and never start the helper.
func Hook(tracker string) string {
	return `if [ -n "${BASH_EXECUTION_STRING+x}" ] && [ -z "${` + Guard + `+x}" ]; then
  case $BASH_EXECUTION_STRING in
  *__codex_snapshot*|*__CODEX_SNAPSHOT*|"if . '"*) ;;
  *)
    export ` + Guard + `=1
    case $BASH_EXECUTION_STRING in
    *[\;\&\|]*|*$'\n'*) . ` + shellsyntax.Quote(tracker) + ` ;;
    esac ;;
  esac
fi
`
}

// Tracker is the Bash source that runs a tracked script. The helper runs as
// a coprocess: the shell sends it segment boundaries and waits for each
// acknowledgment, so the helper has relayed a segment's output before the
// next segment starts. Output reaches Codex unchanged through the helper,
// which alone keeps the original descriptors. A terminal cannot be relayed
// without changing what programs detect, so terminal commands report status
// only.
//
// The rewrite does not run from the startup file, whose errors differ from
// the script's: a fatal expansion error there would abandon the startup file
// and run the original script as well. A one-time DEBUG trap instead runs it
// in place of the script's first command, so error messages, line numbers,
// and fatal errors behave as in the original. Split accepts only scripts
// whose first command fires that trap before it has any effect.
func Tracker(helper, socket, directory string) string {
	return `exec {__mekugi_o}>&1 {__mekugi_x}>&2
coproc __MEKUGI_EXEC { exec ` + shellsyntax.Quote(helper) + ` ` + shellsyntax.Quote(socket) + ` ` + shellsyntax.Quote(directory) + ` "$BASH_EXECUTION_STRING" 3>&"$__mekugi_o" 4>&"$__mekugi_x" 2>/dev/null; } 2>/dev/null
exec {__mekugi_o}>&- {__mekugi_x}>&-
__mekugi_c=${__MEKUGI_EXEC[1]-} __mekugi_a=${__MEKUGI_EXEC[0]-} __mekugi_m= __mekugi_d=
[ -n "$__mekugi_a" ] && IFS=' ' read -r __mekugi_m __mekugi_d <&"$__mekugi_a"
case $__mekugi_m in
relay|status)
  if [ "$__mekugi_m" = relay ]; then
    exec 1>"$__mekugi_d/out" 2>"$__mekugi_d/err"
  fi
  unset -v __mekugi_o __mekugi_x __mekugi_m
  printf 'o\n' >&"$__mekugi_c"
  __mekugi_b() { local __mekugi_s=$? __mekugi_r; printf 'b %s\n' "$1" >&"$__mekugi_c"; IFS= read -r __mekugi_r <&"$__mekugi_a"; return "$__mekugi_s"; }
  __mekugi_e() { local __mekugi_s=$? __mekugi_r; printf 'e %s %s\n' "$1" "$__mekugi_s" >&"$__mekugi_c"; IFS= read -r __mekugi_r <&"$__mekugi_a"; return "$__mekugi_s"; }
  __mekugi_f() { local __mekugi_s=$? __mekugi_r; printf 'd %s\n' "$__mekugi_s" >&"$__mekugi_c"; IFS= read -r __mekugi_r <&"$__mekugi_a"; }
  trap __mekugi_f EXIT
  trap 'trap - DEBUG; eval "$(<"$__mekugi_d/script")"; exit "$?"' DEBUG
  ;;
*)
  [ -n "${__MEKUGI_EXEC_PID-}" ] && wait "$__MEKUGI_EXEC_PID" 2>/dev/null
  unset -v __mekugi_o __mekugi_x __mekugi_c __mekugi_a __mekugi_m __mekugi_d
  ;;
esac
`
}

// Protocol is the helper-to-router message version.
const Protocol = 3

// Timing records helper-observed command boundaries. ElapsedNS is measured
// with the helper's monotonic clock, independently of wall-clock adjustments.
type Timing struct {
	Started   time.Time `json:"started,omitzero"`
	Ended     time.Time `json:"ended,omitzero"`
	ElapsedNS int64     `json:"elapsed_ns,omitzero"`
}

// Message is one line of the helper's report. Hello opens a report and the
// router answers with Reply; the rest follow in the order the shell and its
// output produced them.
type Message struct {
	Timing   Timing   `json:"timing,omitzero"`
	Type     string   `json:"t"`
	Version  int      `json:"v,omitzero"`
	Thread   string   `json:"thread,omitempty"`
	Script   string   `json:"script,omitempty"`
	Segments []string `json:"segments,omitempty"`
	Terminal bool     `json:"tty,omitzero"`
	Index    int      `json:"i,omitzero"`
	Data     string   `json:"d,omitempty"`
	Code     *int     `json:"c,omitempty"`
}

// Message types.
const (
	Hello  = "hello"
	Begin  = "b"
	Output = "o"
	End    = "e"
	Done   = "d"
	// Lossy reports that the helper stopped reporting output to stay within
	// its bound; statuses continue.
	Lossy = "lossy"
)

// Reply accepts or declines a hello.
type Reply struct {
	OK bool `json:"ok"`
}
