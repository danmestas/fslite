package vfs_test

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"


	"github.com/danmestas/go-libfossil"

	"github.com/danmestas/fslite/vfs"
)

// newWritableVFS creates a fresh repo seeded with one commit and returns
// a writable VFS over it. The repo path is reused so the same repo file
// can be reopened (Close + New on the same path) to exercise commit
// roundtrips.
func newWritableVFS(t *testing.T) (string, *vfs.VFS) {
	t.Helper()
	dir := t.TempDir()
	repoPath := filepath.Join(dir, "test.fossil")

	r, err := libfossil.Create(repoPath, libfossil.CreateOpts{User: "test"})
	if err != nil {
		t.Fatalf("libfossil.Create: %v", err)
	}
	_, _, err = r.Commit(libfossil.CommitOpts{
		Files: []libfossil.FileToCommit{
			{Name: "README.md", Content: []byte("seed\n")},
			{Name: "src/main.go", Content: []byte("package main\n")},
		},
		Comment: "initial",
		User:    "test",
		Time:    time.Now().UTC(),
	})
	if err != nil {
		r.Close()
		t.Fatalf("seed Commit: %v", err)
	}
	r.Close()

	v, err := vfs.New(vfs.Config{
		RepoPath:     repoPath,
		Version:      "tip",
		EnableWrites: true,
		User:         "test",
	})
	if err != nil {
		t.Fatalf("vfs.New: %v", err)
	}
	t.Cleanup(func() { v.Close() })
	return repoPath, v
}

func writeViaOpenFile(t *testing.T, v *vfs.VFS, name string, content []byte) {
	t.Helper()
	wf, err := v.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		t.Fatalf("OpenFile(%s): %v", name, err)
	}
	if w, ok := wf.(io.Writer); ok {
		if _, err := w.Write(content); err != nil {
			t.Fatalf("Write(%s): %v", name, err)
		}
	} else {
		t.Fatalf("OpenFile returned %T, not a Writer", wf)
	}
	if err := wf.Close(); err != nil {
		t.Fatalf("Close(%s): %v", name, err)
	}
}

func readAll(t *testing.T, v *vfs.VFS, name string) []byte {
	t.Helper()
	f, err := v.Open(name)
	if err != nil {
		t.Fatalf("Open(%s): %v", name, err)
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("ReadAll(%s): %v", name, err)
	}
	return data
}

func TestOverlayWriteNewFile(t *testing.T) {
	_, v := newWritableVFS(t)

	writeViaOpenFile(t, v, "src/new.go", []byte("package new\n"))

	got := readAll(t, v, "src/new.go")
	if string(got) != "package new\n" {
		t.Errorf("got %q want %q", got, "package new\n")
	}
}

func TestOverlayModifyExisting(t *testing.T) {
	_, v := newWritableVFS(t)

	writeViaOpenFile(t, v, "README.md", []byte("changed\n"))

	got := readAll(t, v, "README.md")
	if string(got) != "changed\n" {
		t.Errorf("got %q want %q", got, "changed\n")
	}
}

func TestOverlayDeleteHidesFromStat(t *testing.T) {
	_, v := newWritableVFS(t)

	if err := v.Remove("README.md"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := v.Stat("README.md"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("after Remove: want fs.ErrNotExist, got %v", err)
	}
}

func TestOverlayDeleteHidesFromReadDir(t *testing.T) {
	_, v := newWritableVFS(t)

	if err := v.Remove("README.md"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	entries, err := v.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if e.Name() == "README.md" {
			t.Errorf("README.md still appears in ReadDir after Remove")
		}
	}
}

func TestOverlayAddShowsInReadDir(t *testing.T) {
	_, v := newWritableVFS(t)

	writeViaOpenFile(t, v, "newdir/added.txt", []byte("hi\n"))

	rootEntries, err := v.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir(.): %v", err)
	}
	found := false
	for _, e := range rootEntries {
		if e.Name() == "newdir" && e.IsDir() {
			found = true
		}
	}
	if !found {
		t.Errorf("synthetic dir 'newdir' missing from root ReadDir: %v", names(rootEntries))
	}

	subEntries, err := v.ReadDir("newdir")
	if err != nil {
		t.Fatalf("ReadDir(newdir): %v", err)
	}
	if len(subEntries) != 1 || subEntries[0].Name() != "added.txt" {
		t.Errorf("newdir ReadDir: got %v want [added.txt]", names(subEntries))
	}
}

// TestCommitRoundtripPersists is the e2e write path: write, commit, reopen.
// After Commit the data must live in the Fossil manifest at the new tip
// (i.e. survive closing and reopening the VFS).
func TestCommitRoundtripPersists(t *testing.T) {
	repoPath, v := newWritableVFS(t)

	writeViaOpenFile(t, v, "src/new.go", []byte("package new\n"))
	writeViaOpenFile(t, v, "README.md", []byte("changed\n"))

	ckin, err := v.Commit("write through")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if ckin.UUID == "" || ckin.RID == 0 {
		t.Fatalf("Commit returned empty checkin: %+v", ckin)
	}
	v.Close()

	v2, err := vfs.New(vfs.Config{RepoPath: repoPath, Version: "tip"})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer v2.Close()

	if got := readAll(t, v2, "src/new.go"); string(got) != "package new\n" {
		t.Errorf("post-reopen src/new.go: got %q", got)
	}
	if got := readAll(t, v2, "README.md"); string(got) != "changed\n" {
		t.Errorf("post-reopen README.md: got %q", got)
	}
}

func TestCommitRoundtripWithDelete(t *testing.T) {
	repoPath, v := newWritableVFS(t)

	if err := v.Remove("README.md"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	writeViaOpenFile(t, v, "src/added.go", []byte("package added\n"))

	if _, err := v.Commit("delete + add"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	v.Close()

	v2, err := vfs.New(vfs.Config{RepoPath: repoPath, Version: "tip"})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer v2.Close()

	if _, err := v2.Stat("README.md"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("README.md still present after committed delete: %v", err)
	}
	if got := readAll(t, v2, "src/added.go"); string(got) != "package added\n" {
		t.Errorf("post-reopen src/added.go: got %q", got)
	}
	// src/main.go must survive — it was untouched in the commit.
	if got := readAll(t, v2, "src/main.go"); string(got) != "package main\n" {
		t.Errorf("post-reopen src/main.go (untouched): got %q", got)
	}
}

func TestRename(t *testing.T) {
	_, v := newWritableVFS(t)

	if err := v.Rename("README.md", "READING.md"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if _, err := v.Stat("README.md"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("old name still exists after rename")
	}
	if got := readAll(t, v, "READING.md"); string(got) != "seed\n" {
		t.Errorf("new name content: got %q", got)
	}
}

func TestMkdirIsNoOp(t *testing.T) {
	_, v := newWritableVFS(t)

	if err := v.Mkdir("freshdir", 0755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	// Writing under the dir should work without further setup.
	writeViaOpenFile(t, v, "freshdir/x.txt", []byte("x\n"))
	if got := readAll(t, v, "freshdir/x.txt"); string(got) != "x\n" {
		t.Errorf("write-under-mkdir failed: %q", got)
	}
}

func TestWriteDisabledReturnsReadOnly(t *testing.T) {
	repoPath, v := newWritableVFS(t)
	v.Close()

	ro, err := vfs.New(vfs.Config{RepoPath: repoPath, Version: "tip"}) // EnableWrites false
	if err != nil {
		t.Fatalf("read-only New: %v", err)
	}
	defer ro.Close()

	_, err = ro.OpenFile("anything.txt", os.O_WRONLY|os.O_CREATE, 0644)
	if err == nil {
		t.Fatal("expected error on OpenFile for write without EnableWrites")
	}
}

// TestOverlayPersistsAcrossReopen verifies that overlay rows survive a VFS
// Close+New on the same repo file — the overlay is durable SQLite state.
func TestOverlayPersistsAcrossReopen(t *testing.T) {
	repoPath, v := newWritableVFS(t)

	writeViaOpenFile(t, v, "src/draft.go", []byte("draft\n"))
	v.Close()

	v2, err := vfs.New(vfs.Config{
		RepoPath:     repoPath,
		Version:      "tip",
		EnableWrites: true,
	})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer v2.Close()

	if got := readAll(t, v2, "src/draft.go"); string(got) != "draft\n" {
		t.Errorf("overlay did not persist: got %q", got)
	}
}

func names(entries []fs.DirEntry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Name()
	}
	return out
}
