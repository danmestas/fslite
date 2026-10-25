package vfs

import (
	"container/list"
	"sync"
)

// DefaultCacheBytes is the blob cache budget when Config.CacheBytes is 0.
const DefaultCacheBytes int64 = 64 << 20

// blobCache is an LRU of expanded file content keyed by artifact hash and
// bounded in bytes. Content is immutable once stored, and the hash names
// the bytes, so an entry never goes stale: it survives commits, pulls and
// renames, which only change which path points at which hash. Callers that
// mutate what they read must copy first.
type blobCache struct {
	mu    sync.Mutex
	limit int64
	used  int64
	order *list.List // front is most recently used
	items map[string]*list.Element
}

type blobCacheEntry struct {
	uuid    string
	content []byte
}

// newBlobCache returns a cache holding at most limit bytes of content. A
// limit of zero or less disables caching: every get misses and put drops.
func newBlobCache(limit int64) *blobCache {
	return &blobCache{limit: limit, order: list.New(), items: map[string]*list.Element{}}
}

func (c *blobCache) get(uuid string) ([]byte, bool) {
	if c == nil || c.limit <= 0 || uuid == "" {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[uuid]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(el)
	return el.Value.(*blobCacheEntry).content, true
}

// put stores content under uuid, evicting the least recently used entries
// until it fits. Content larger than the whole budget is not cached.
func (c *blobCache) put(uuid string, content []byte) {
	if c == nil || c.limit <= 0 || uuid == "" || int64(len(content)) > c.limit {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[uuid]; ok {
		c.order.MoveToFront(el)
		return
	}
	for c.used+int64(len(content)) > c.limit {
		back := c.order.Back()
		if back == nil {
			break
		}
		c.evict(back)
	}
	el := c.order.PushFront(&blobCacheEntry{uuid: uuid, content: content})
	c.items[uuid] = el
	c.used += int64(len(content))
}

func (c *blobCache) evict(el *list.Element) {
	e := el.Value.(*blobCacheEntry)
	c.order.Remove(el)
	delete(c.items, e.uuid)
	c.used -= int64(len(e.content))
}

// stats reports the entry count and bytes held, for tests.
func (c *blobCache) stats() (entries int, bytes int64) {
	if c == nil {
		return 0, 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items), c.used
}
