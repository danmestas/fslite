package vfs_test

import (
	"errors"
	"io/fs"
	"testing"

	"github.com/danmestas/fslite/vfs"
)

// Stat and ReadDir report each committed file's size from the repository's
// recorded blob size, not from a content read. The sizes must agree with the
// content for ordinary and executable files alike.
func TestStatSizesMatchContent(t *testing.T) {
	v := newVFS(t, "tip")
	want := map[string]int64{
		"README.md":          int64(len("# project\n")),
		"src/main.go":        int64(len("package main\n")),
		"src/util/helper.go": int64(len("package util\n")),
		"docs/intro.md":      int64(len("intro\n")),
		"bin/run":            int64(len("#!/bin/sh\necho hi\n")),
	}
	for name, size := range want {
		fi, err := v.Stat(name)
		if err != nil {
			t.Fatalf("Stat(%s): %v", name, err)
		}
		if fi.Size() != size {
			t.Errorf("Stat(%s).Size = %d, want %d", name, fi.Size(), size)
		}
	}
	entries, err := v.ReadDir("src")
	if err != nil {
		t.Fatalf("ReadDir(src): %v", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			t.Fatalf("Info(%s): %v", e.Name(), err)
		}
		if fi.Size() != want["src/"+e.Name()] {
			t.Errorf("ReadDir Info(%s).Size = %d, want %d", e.Name(), fi.Size(), want["src/"+e.Name()])
		}
	}
}

// After a commit the view re-roots at the new check-in; sizes follow the
// new content, and files carried forward keep theirs.
func TestStatSizeTracksCommits(t *testing.T) {
	_, v := newWritableVFS(t)
	if fi, err := v.Stat("README.md"); err != nil || fi.Size() != int64(len("seed\n")) {
		t.Fatalf("seed README.md: size %v, err %v", fi, err)
	}
	writeViaOpenFile(t, v, "README.md", []byte("a longer readme\n"))
	if fi, err := v.Stat("README.md"); err != nil || fi.Size() != int64(len("a longer readme\n")) {
		t.Fatalf("overlay README.md: %v, %v", fi, err)
	}
	if _, err := v.Commit("grow readme"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	fi, err := v.Stat("README.md")
	if err != nil || fi.Size() != int64(len("a longer readme\n")) {
		t.Errorf("committed README.md: size %d, err %v", fi.Size(), err)
	}
	fi, err = v.Stat("src/main.go")
	if err != nil || fi.Size() != int64(len("package main\n")) {
		t.Errorf("carried src/main.go: size %d, err %v", fi.Size(), err)
	}
}

// A rename of an overlay-only file over a tracked one is an editor's
// atomic save. It commits as one changed file, and the stale tombstone
// is gone afterwards.
func TestCommitAfterAtomicSave(t *testing.T) {
	repoPath, v := newWritableVFS(t)
	writeViaOpenFile(t, v, ".README.md.tmp", []byte("saved atomically\n"))
	if err := v.Rename(".README.md.tmp", "README.md"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if _, err := v.Commit("atomic save"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if _, err := v.Stat(".README.md.tmp"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("temp path should not exist after commit: %v", err)
	}
	v.Close()

	v2, err := vfs.New(vfs.Config{RepoPath: repoPath, Version: "tip"})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer v2.Close()
	if got := readAll(t, v2, "README.md"); string(got) != "saved atomically\n" {
		t.Errorf("README.md = %q", got)
	}
	if got := readAll(t, v2, "src/main.go"); string(got) != "package main\n" {
		t.Errorf("src/main.go carried forward = %q", got)
	}
}

// Deleting a file that was created in the overlay and never committed
// leaves nothing to record.
func TestCommitOnlyStaleTombstoneIsNothingToCommit(t *testing.T) {
	_, v := newWritableVFS(t)
	writeViaOpenFile(t, v, "scratch.txt", []byte("temp\n"))
	if err := v.Remove("scratch.txt"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := v.Commit("nothing"); !errors.Is(err, vfs.ErrNothingToCommit) {
		t.Errorf("Commit = %v, want ErrNothingToCommit", err)
	}
	if _, err := v.Stat("scratch.txt"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("scratch.txt should be gone: %v", err)
	}
}
