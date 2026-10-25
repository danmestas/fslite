package vfs

import (
	"bytes"
	"testing"
)

func TestBlobCacheEvictsLeastRecentlyUsedByBytes(t *testing.T) {
	c := newBlobCache(10)
	c.put("a", []byte("aaaa"))
	c.put("b", []byte("bbbb"))
	if _, ok := c.get("a"); !ok {
		t.Fatal("a should be cached")
	}
	// 4+4+4 > 10: the least recently used entry, b, goes; a was just touched.
	c.put("c", []byte("cccc"))
	if _, ok := c.get("b"); ok {
		t.Error("b should have been evicted")
	}
	if got, ok := c.get("a"); !ok || !bytes.Equal(got, []byte("aaaa")) {
		t.Errorf("a = %q, %v; want kept", got, ok)
	}
	if got, ok := c.get("c"); !ok || !bytes.Equal(got, []byte("cccc")) {
		t.Errorf("c = %q, %v; want kept", got, ok)
	}
	if n, used := c.stats(); n != 2 || used != 8 {
		t.Errorf("stats = %d entries, %d bytes; want 2, 8", n, used)
	}
}

func TestBlobCacheRefusesOversizeAndDisabled(t *testing.T) {
	c := newBlobCache(3)
	c.put("big", []byte("toolarge"))
	if _, ok := c.get("big"); ok {
		t.Error("content larger than the budget must not be cached")
	}
	off := newBlobCache(0)
	off.put("x", []byte("x"))
	if _, ok := off.get("x"); ok {
		t.Error("a zero budget disables the cache")
	}
	var nilCache *blobCache
	nilCache.put("x", []byte("x"))
	if _, ok := nilCache.get("x"); ok {
		t.Error("a nil cache must behave as disabled")
	}
	c.put("", []byte("k"))
	if n, _ := c.stats(); n != 0 {
		t.Error("an empty key is never cached")
	}
}
