package vfs

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/danmestas/go-libfossil"
)

// A node looked up before a commit re-rooted the view must still read its
// own artifact, and only that artifact may be cached under its hash: the
// cache is keyed by hash, and a wrong entry would be served to every later
// read of that hash. The test takes a node at "one", commits "TWO-longer",
// reads through the stale node with nothing cached and expects "one", then
// commits "one" again so the path's hash is the stale node's once more, and
// expects "one" back.
func TestReadBlobDoesNotCacheAnotherArtifactsBytes(t *testing.T) {
	v := newSeededVFS(t, "a.txt", "one\n")

	stale := v.lookup("a.txt")
	if stale == nil || stale.uuid == "" {
		t.Fatal("a.txt has no committed node")
	}
	commitContent(t, v, "a.txt", "TWO-longer\n")
	// The write path has already cached "one" under the stale hash. Start
	// the stale read from an empty cache, as after an eviction, or a node
	// nothing has read yet.
	v.blobs = newBlobCache(cacheBudget(0))
	v.sizeMu.Lock()
	v.sizes = make(map[string]int64)
	v.sizeMu.Unlock()
	if got, err := v.readBlob("a.txt", stale); err != nil {
		t.Fatalf("readBlob(stale): %v", err)
	} else if string(got) != "one\n" {
		t.Errorf("stale node read %q, want its own artifact %q", got, "one\n")
	}
	commitContent(t, v, "a.txt", "one\n")

	got, err := v.readBlob("a.txt", v.lookup("a.txt"))
	if err != nil {
		t.Fatalf("readBlob: %v", err)
	}
	if string(got) != "one\n" {
		t.Errorf("a.txt = %q, want %q: the stale read cached another artifact's bytes", got, "one\n")
	}
	size, err := v.fileSize("a.txt", v.lookup("a.txt"))
	if err != nil {
		t.Fatalf("fileSize: %v", err)
	}
	if size != int64(len("one\n")) {
		t.Errorf("a.txt size = %d, want %d", size, len("one\n"))
	}
}

// newSeededVFS returns a writable VFS over a new repository whose one
// check-in holds name with content.
func newSeededVFS(t *testing.T, name, content string) *VFS {
	t.Helper()
	repoPath := filepath.Join(t.TempDir(), "test.fossil")
	r, err := libfossil.Create(repoPath, libfossil.CreateOpts{User: "test"})
	if err != nil {
		t.Fatalf("libfossil.Create: %v", err)
	}
	if _, _, err := r.Commit(libfossil.CommitOpts{
		Files:   []libfossil.FileToCommit{{Name: name, Content: []byte(content)}},
		Comment: "initial",
		User:    "test",
		Time:    time.Now().UTC(),
	}); err != nil {
		r.Close()
		t.Fatalf("seed Commit: %v", err)
	}
	r.Close()

	v, err := New(Config{RepoPath: repoPath, Version: "tip", EnableWrites: true, User: "test"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { v.Close() })
	return v
}

func writeContent(t *testing.T, v *VFS, name, content string) {
	t.Helper()
	f, err := v.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		t.Fatalf("OpenFile(%s): %v", name, err)
	}
	w, ok := f.(io.Writer)
	if !ok {
		t.Fatalf("OpenFile returned %T, not a Writer", f)
	}
	if _, err := w.Write([]byte(content)); err != nil {
		t.Fatalf("Write(%s): %v", name, err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close(%s): %v", name, err)
	}
}

func commitContent(t *testing.T, v *VFS, name, content string) {
	t.Helper()
	writeContent(t, v, name, content)
	if _, err := v.Commit("set " + name); err != nil {
		t.Fatalf("Commit: %v", err)
	}
}
