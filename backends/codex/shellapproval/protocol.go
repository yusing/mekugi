// Package shellapproval defines the private command and askpass decision protocol.
package shellapproval

// Source: internal/vcsguard/vcsguard.go:26:62@[bff7c655] approval wire.
// Source: internal/sudoask/sudo.go:11:20@[bff7c655] askpass resources.
import (
	"path/filepath"
	"time"

	"github.com/yusing/mekugi/libraries/shellinstrumentation"
)

// Timeout bounds how long a guarded command waits for an answer. A command
// that receives none is denied.
const Timeout = 5 * time.Minute

// Channel names the session-local Unix approval socket.
const Channel = "vcs-approval.sock"

// Message asks the router to approve one remote write.
type Message struct {
	Kind       string   `json:"kind,omitempty"` // sudo command or sudo-password; empty for remote writes.
	Thread     string   `json:"thread,omitempty"`
	Item       string   `json:"item,omitempty"`
	Cwd        string   `json:"cwd,omitempty"`
	Argv       []string `json:"argv"`
	Executable string   `json:"executable"` // Resolved tool, separate from its display name.
}

// Reply answers a Message. Reason explains a denial to the command's stderr.
type Reply struct {
	Password string `json:"password,omitempty"` // Askpass pipe only; never retained.
	OK       bool   `json:"ok"`
	Reason   string `json:"reason,omitempty"`
}

// MaxMessage bounds one approval request or reply.
const MaxMessage = 1 << 20

// Paths returns the guard directory and approval channel for a frontend
// directory.
func Paths(frontendDirectory string) (directory, channel string) {
	root := filepath.Dir(frontendDirectory)
	return filepath.Join(root, shellinstrumentation.VCSDirectory), filepath.Join(root, Channel)
}

// ChannelOf returns the approval channel beside a guard directory.
func ChannelOf(directory string) string {
	return filepath.Join(filepath.Dir(directory), Channel)
}

const (
	Askpass              = "mekugi-sudo-askpass"
	DirectoryEnvironment = "MEKUGI_SUDO_DIRECTORY"
	CommandEnvironment   = "MEKUGI_SUDO_COMMAND"
)

func SudoDirectory(frontend string) string {
	return filepath.Join(filepath.Dir(frontend), shellinstrumentation.SudoDirectory)
}
