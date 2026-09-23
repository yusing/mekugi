package router

import (
	"container/list"
	"sync"
)

const maxExecLastSeenBytes = 64 << 20
const maxExecLastSeenFiles = 4096

type execPrior struct {
	file   execFileSnapshot
	change string
}
type execCachedFile struct {
	key   string
	prior execPrior
	bytes int
}

// This optional process-local cache is not a call baseline or replay authority.
// Every diff derived from it names the earlier observation explicitly.
type execLastSeen struct {
	mu    sync.Mutex
	files map[string]*list.Element
	order list.List
	bytes int
}

func (c *execLastSeen) get(namespace, path string) (execPrior, bool) {
	if c == nil {
		return execPrior{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	element := c.files[namespace+"\x00"+path]
	if element == nil {
		return execPrior{}, false
	}
	c.order.MoveToFront(element)
	return element.Value.(execCachedFile).prior, true
}

func (c *execLastSeen) put(namespace, change string, file execFileSnapshot) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	key := namespace + "\x00" + file.Path
	if old := c.files[key]; old != nil {
		c.bytes -= old.Value.(execCachedFile).bytes
		c.order.Remove(old)
		delete(c.files, key)
	}
	if file.Error != "" || file.Kind != execFileText && file.Kind != execFileSymlink {
		return
	}
	size := len(file.Content) + len(file.Link) + len(key) + len(change) + 256
	if size > maxExecLastSeenBytes {
		return
	}
	if c.files == nil {
		c.files = make(map[string]*list.Element)
	}
	c.files[key] = c.order.PushFront(execCachedFile{key: key, prior: execPrior{file: file, change: change}, bytes: size})
	c.bytes += size
	for c.bytes > maxExecLastSeenBytes || len(c.files) > maxExecLastSeenFiles {
		old := c.order.Back()
		entry := old.Value.(execCachedFile)
		delete(c.files, entry.key)
		c.bytes -= entry.bytes
		c.order.Remove(old)
	}
}
