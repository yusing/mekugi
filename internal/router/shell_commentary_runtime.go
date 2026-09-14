package router

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/yusing/mekugi/internal/shellruntime"
)

type shellCommentaryDescriptor struct {
	Endpoint string `json:"endpoint"`
	Token    string `json:"token"`
	Worker   string `json:"worker"`
}

type ownedShellCommentary struct {
	name     string
	identity os.FileInfo
	content  []byte
}

// Commentary discovery is auxiliary: unavailable or replaced storage never
// prevents a shell command from running.
func (p *mekugiProxy) prepareShellCommentary(threadID, historySessionID, author string) {
	if p.commentaryEndpoint == "" {
		return
	}
	// Shell authors persist across turns. Reject malformed names before they
	// acquire provenance, independently of optional root-projection ancestry.
	if strings.ContainsAny(author, "\r\n\x00") {
		return
	}
	directory, err := shellruntime.ScriptsPath(p.shellDirectory, threadID)
	if err != nil {
		return
	}
	token := p.commentary.subscribeThread(historySessionID, threadID, author)
	if token == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	session := p.shellSessions[directory]
	if p.closed || session == nil {
		return
	}
	target, err := session.parent.Readlink(session.runtimeName)
	if err != nil || target != session.runtimeTarget {
		return
	}
	content, err := json.Marshal(shellCommentaryDescriptor{Endpoint: p.commentaryEndpoint, Token: token, Worker: target})
	if err != nil {
		return
	}
	if owned := session.commentary; owned != nil {
		if bytes.Equal(owned.content, content) {
			return
		}
		file, err := session.parent.OpenFile(owned.name, os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if err != nil {
			return
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() || !os.SameFile(owned.identity, info) {
			return
		}
		current, err := io.ReadAll(io.LimitReader(file, 16<<10))
		if err != nil || !bytes.Equal(current, owned.content) {
			return
		}
		// Write through the verified descriptor, never reopen the pathname.
		if _, err := file.WriteAt(content, 0); err != nil {
			return
		}
		if err := file.Truncate(int64(len(content))); err != nil {
			return
		}
		owned.content = content
		return
	}
	name := session.runtimeName + ".commentary"
	file, err := session.parent.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return
	}
	identity, statErr := file.Stat()
	_, writeErr := file.Write(content)
	closeErr := file.Close()
	if statErr != nil {
		return
	}
	session.commentary = &ownedShellCommentary{name: name, identity: identity, content: content}
	if writeErr != nil || closeErr != nil {
		return
	}
}

func readShellCommentary(parent *os.Root, name string) ([]byte, os.FileInfo, error) {
	file, err := parent.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return nil, nil, os.ErrPermission
	}
	content, err := io.ReadAll(io.LimitReader(file, 16<<10))
	return content, info, err
}

func (s *shellSession) closeCommentary() error {
	if s.commentary == nil {
		return nil
	}
	owned := s.commentary
	content, info, err := readShellCommentary(s.parent, owned.name)
	if err != nil || !os.SameFile(owned.identity, info) || !bytes.Equal(content, owned.content) {
		return nil
	}
	// Unlink only; a replacement directory is never traversed. The private
	// random token also guards against reuse of a historical inode number.
	return s.parent.Remove(owned.name)
}

func discoverShellCommentary(worker string) shellCommentarySink {
	directory, err := shellruntime.Directory()
	if err != nil {
		return nil
	}
	runtimePath, err := shellruntime.Path(directory, os.Getenv(shellruntime.ThreadIDEnvironment))
	if err != nil {
		return nil
	}
	parent, err := os.OpenRoot(directory)
	if err != nil {
		return nil
	}
	defer parent.Close()
	name := filepath.Base(runtimePath)
	content, _, err := readShellCommentary(parent, name+".commentary")
	if err != nil {
		return nil
	}
	var descriptor shellCommentaryDescriptor
	if json.Unmarshal(content, &descriptor) != nil || descriptor.Token == "" {
		return nil
	}
	endpoint, err := url.Parse(descriptor.Endpoint)
	if err != nil || endpoint.Scheme != "http" || endpoint.Host == "" || endpoint.User != nil {
		return nil
	}
	target, err := parent.Readlink(name)
	if err != nil || target != descriptor.Worker || target != worker {
		return nil
	}
	return &threadShellCommentarySink{httpShellCommentarySink{endpoint: descriptor.Endpoint, token: descriptor.Token, client: commentaryHTTPClient}}
}

type threadShellCommentarySink struct{ httpShellCommentarySink }

// A worker ends, but the shared thread route remains available to other workers.
func (*threadShellCommentarySink) Complete(context.Context) error { return nil }
