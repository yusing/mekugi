package execsegment

import (
	"time"

	"github.com/yusing/mekugi/libraries/commandanalysis/shellsyntax"
)

// Guard marks a shell that already decided whether to track its command, so
// nested command shells started by that command are never tracked.
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
// otherwise record it for every later command.
func Hook(tracker string) string {
	// Codex injects the executing thread into tool commands, not hook handlers.
	return `if [ -n "${CODEX_THREAD_ID-}" ] && [ -n "${BASH_EXECUTION_STRING+x}" ] && [ -z "${` + Guard + `+x}" ]; then
  case $BASH_EXECUTION_STRING in
  *__codex_snapshot*|*__CODEX_SNAPSHOT*|"if . '"*) ;;
  *)
    export ` + Guard + `=1
    # Starting the coprocess changes these observations even if it declines.
    case $BASH_EXECUTION_STRING in
    *'$!'*|*'${!'*|*'$_'*|*'${_'*|*'PIPESTATUS'*|*'LINENO'*|*'BASH_COMMAND'*|*'BASH_SOURCE'*|*'FUNCNAME'*|*'BASH_EXECUTION_STRING'*) ;;
    *)
      # Coprocesses and dynamic descriptors require Bash 4.1 or newer.
      if (( BASH_VERSINFO[0] > 4 || (BASH_VERSINFO[0] == 4 && BASH_VERSINFO[1] >= 1) )); then
        . ` + shellsyntax.Quote(tracker) + `
      fi ;;
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
func Tracker(helper, channel, directory string) string {
	return `exec {__mekugi_o}>&1 {__mekugi_x}>&2
coproc __MEKUGI_EXEC { exec ` + shellsyntax.Quote(helper) + ` ` + shellsyntax.Quote(channel) + ` ` + shellsyntax.Quote(directory) + ` "$BASH_EXECUTION_STRING" 3>&"$__mekugi_o" 4>&"$__mekugi_x" 2>/dev/null; } 2>/dev/null
__mekugi_c=${__MEKUGI_EXEC[1]-} __mekugi_a=${__MEKUGI_EXEC[0]-} __mekugi_m= __mekugi_d= __mekugi_p= __mekugi_y= __mekugi_z=
[ -n "$__mekugi_a" ] && IFS=' ' read -r __mekugi_m __mekugi_d <&"$__mekugi_a"
case $__mekugi_m in
observe) ;;
relay|status)
  if ! { __mekugi_p=$(<"$__mekugi_d/script") && [ -n "$__mekugi_p" ]; } 2>/dev/null; then
    __mekugi_m=
  elif [ "$__mekugi_m" = relay ]; then
    if ! { exec {__mekugi_y}>"$__mekugi_d/out" {__mekugi_z}>"$__mekugi_d/err"; } 2>/dev/null ||
       [ ! -p "/dev/fd/$__mekugi_y" ] || [ ! -p "/dev/fd/$__mekugi_z" ]; then
      __mekugi_m=
    else
      exec 1>&"$__mekugi_y" 2>&"$__mekugi_z"
    fi
  fi
  ;;
esac
exec {__mekugi_o}>&- {__mekugi_x}>&-
[ -n "$__mekugi_y" ] && exec {__mekugi_y}>&-
[ -n "$__mekugi_z" ] && exec {__mekugi_z}>&-
unset -v __mekugi_y __mekugi_z
case $__mekugi_m in
observe|relay|status)
  unset -v __mekugi_o __mekugi_x __mekugi_m
` + mktempHook(helper, `"$__mekugi_t/$__mekugi_c"`, `
    # Subshells and pipelines close coprocess descriptors, and a duplicate
    # does not reach children. $$ names the shell that keeps the pipe open.
    __mekugi_t=/proc/$$/fd
    [ -d "$__mekugi_t" ] || __mekugi_t=/dev/fd`, true) + `
  printf 'o\n' >&"$__mekugi_c"
  __mekugi_b() { local __mekugi_s=$? __mekugi_r; printf 'b %s\n' "$1" >&"$__mekugi_c"; IFS= read -r __mekugi_r <&"$__mekugi_a"; return "$__mekugi_s"; }
  __mekugi_e() { local __mekugi_s=$? __mekugi_r; printf 'e %s %s\n' "$1" "$__mekugi_s" >&"$__mekugi_c"; IFS= read -r __mekugi_r <&"$__mekugi_a"; return "$__mekugi_s"; }
  __mekugi_f() { local __mekugi_s=$? __mekugi_r; printf 'd %s\n' "$__mekugi_s" >&"$__mekugi_c"; IFS= read -r __mekugi_r <&"$__mekugi_a"; }
  trap __mekugi_f EXIT
  if [ -n "$__mekugi_p" ]; then
    trap 'trap - DEBUG; eval "$__mekugi_p"; exit "$?"' DEBUG
  else
    # A single command runs unchanged after startup. Its EXIT boundary
    # reports timing only; stdout and stderr keep their original descriptors.
    __mekugi_b 0
  fi
  ;;
*)
  [ -n "$__mekugi_c" ] && exec {__mekugi_c}>&-
  [ -n "$__mekugi_a" ] && exec {__mekugi_a}<&-
  [ -n "${__MEKUGI_EXEC_PID-}" ] && wait "$__MEKUGI_EXEC_PID" 2>/dev/null
  unset -v __mekugi_o __mekugi_x __mekugi_c __mekugi_a __mekugi_m __mekugi_d __mekugi_p
  ;;
esac
`
}

// mktempHook reports the utility's result, not a predicted temporary name.
// The sentinel keeps command substitution from removing output newlines.
// Reporting never changes the utility's output, diagnostics, or status.
//
// Bash also accepts function names with slashes, so absolute invocations of
// mktemp from PATH, /bin, or /usr/bin are reported as well. Wrapping command
// moves the line number in its diagnostics into the wrapper, so it is wrapped
// only when the script names both. Dash rejects such names and cannot see the
// script, so it reports bare mktemp only.
func mktempHook(helper, control, setup string, bash bool) string {
	run, extra := "command", ""
	if bash {
		run = "builtin command"
		extra = `
    __mekugi_k=/bin:/usr/bin:$PATH:
    while [ -n "$__mekugi_k" ]; do
      __mekugi_j=${__mekugi_k%%:*}/mktemp __mekugi_k=${__mekugi_k#*:}
      case $__mekugi_j in
      /*[!A-Za-z0-9/._+-]*) ;;
      /*) [ -x "$__mekugi_j" ] && eval "$__mekugi_j() { __mekugi_mktemp $__mekugi_j \"\$@\"; }" ;;
      esac
    done
    unset -v __mekugi_j __mekugi_k
    case $BASH_EXECUTION_STRING in
    *command*mktemp*)
      declare -F command >/dev/null || command() {
        case $1 in -p|--) set -- "$2" "$@" ;; *) set -- "$1" "$@" ;; esac
        case $1 in
        mktemp|/*/mktemp) shift; __mekugi_mktemp "$@" ;;
        *) shift; builtin command "$@" ;;
        esac
      }
      ;;
    esac`
	}
	return `  if [ "$(command -v mktemp)" != mktemp ]; then` + setup + `
    __mekugi_mktemp() {
      local __mekugi_result __mekugi_status
      if __mekugi_result=$(` + run + ` "$@"; __mekugi_status=$?; printf .; exit "$__mekugi_status"); then
        __mekugi_status=0
      else
        __mekugi_status=$?
      fi
      __mekugi_result=${__mekugi_result%.}
      if [ "$__mekugi_status" -eq 0 ]; then
        ` + shellsyntax.Quote(helper) + ` --created-path "$__mekugi_result" 2>/dev/null 3>` + control + ` || :
      fi
      printf '%s' "$__mekugi_result"
      return "$__mekugi_status"
    }
    mktemp() { __mekugi_mktemp mktemp "$@"; }` + extra + `
  fi
`
}

// Protocol is the helper-to-router message version.
const Protocol = 4

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
	Tree     bool     `json:"tree,omitzero"`
	Index    int      `json:"i,omitzero"`
	Data     string   `json:"d,omitempty"`
	Code     *int     `json:"c,omitempty"`
}

// Message types.
const (
	Hello   = "hello"
	Begin   = "b"
	Output  = "o"
	Created = "c"
	End     = "e"
	Done    = "d"
	// Closed follows all output and ends the report without waiting for FIFO EOF.
	Closed = "closed"
	// Lossy reports that the helper stopped reporting output to stay within
	// its bound; statuses continue.
	Lossy = "lossy"
)

// Reply accepts or declines a hello.
type Reply struct {
	OK bool `json:"ok"`
}
