package vfs_test

import (
	"io"
	"io/fs"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"


	"github.com/danmestas/libfossil"

	"github.com/danmestas/fslite/vfs"
)

// createTestRepo creates a Fossil repo with a multi-directory tree and
// returns its on-disk path.
func createTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	repoPath := filepath.Join(dir, "test.fossil")

	r, err := libfossil.Create(repoPath, libfossil.CreateOpts{User: "test"})
	if err != nil {
		t.Fatalf("libfossil.Create: %v", err)
	}
	defer r.Close()

	_, _, err = r.Commit(libfossil.CommitOpts{
		Files: []libfossil.FileToCommit{
			{Name: "README.md", Content: []byte("# project\n")},
			{Name: "src/main.go", Content: []byte("package main\n")},
			{Name: "src/util/helper.go", Content: []byte("package util\n")},
			{Name: "docs/intro.md", Content: []byte("intro\n")},
			{Name: "bin/run", Content: []byte("#!/bin/sh\necho hi\n"), Perm: "x"},
		},
		Comment: "initial",
		User:    "test",
		Time:    time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	return repoPath
}

func newVFS(t *testing.T, version string) *vfs.VFS {
	t.Helper()
	v, err := vfs.New(vfs.Config{
		RepoPath: createTestRepo(t),
		Version:  version,
	})
	if err != nil {
		t.Fatalf("vfs.New: %v", err)
	}
	t.Cleanup(func() { v.Close() })
	return v
}

func TestReadDirRoot(t *testing.T) {
	v := newVFS(t, "tip")

	entries, err := v.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}

	gotNames := make(map[string]bool, len(entries))
	for _, e := range entries {
		gotNames[e.Name()] = e.IsDir()
	}
	wantDirs := []string{"src", "docs", "bin"}
	wantFiles := []string{"README.md"}
	for _, d := range wantDirs {
		if isDir, ok := gotNames[d]; !ok {
			t.Errorf("missing dir %q", d)
		} else if !isDir {
			t.Errorf("%q expected dir, got file", d)
		}
	}
	for _, f := range wantFiles {
		if isDir, ok := gotNames[f]; !ok {
			t.Errorf("missing file %q", f)
		} else if isDir {
			t.Errorf("%q expected file, got dir", f)
		}
	}
}

func TestReadDirSubtree(t *testing.T) {
	v := newVFS(t, "tip")

	entries, err := v.ReadDir("src")
	if err != nil {
		t.Fatalf("ReadDir src: %v", err)
	}
	got := make(map[string]bool)
	for _, e := range entries {
		got[e.Name()] = e.IsDir()
	}
	if isDir, ok := got["main.go"]; !ok {
		t.Errorf("src/main.go missing: %v", got)
	} else if isDir {
		t.Errorf("src/main.go expected file, got dir")
	}
	if isDir, ok := got["util"]; !ok {
		t.Errorf("src/util missing: %v", got)
	} else if !isDir {
		t.Errorf("src/util expected dir, got file")
	}

	utilEntries, err := v.ReadDir("src/util")
	if err != nil {
		t.Fatalf("ReadDir src/util: %v", err)
	}
	if len(utilEntries) != 1 || utilEntries[0].Name() != "helper.go" {
		t.Errorf("expected [helper.go], got %v", utilEntries)
	}
}

func TestOpenAndReadFile(t *testing.T) {
	v := newVFS(t, "tip")

	f, err := v.Open("src/util/helper.go")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()

	content, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(content) != "package util\n" {
		t.Errorf("got %q want %q", content, "package util\n")
	}

	info, err := f.Stat()
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Size() != int64(len("package util\n")) {
		t.Errorf("size: got %d want %d", info.Size(), len("package util\n"))
	}
	if info.IsDir() {
		t.Error("file Stat reports IsDir true")
	}
}

func TestStatFile(t *testing.T) {
	v := newVFS(t, "tip")

	info, err := v.Stat("README.md")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.IsDir() {
		t.Error("README.md IsDir true")
	}
	if info.Size() != int64(len("# project\n")) {
		t.Errorf("size: got %d want %d", info.Size(), len("# project\n"))
	}
	if info.Mode().Perm() != 0644 {
		t.Errorf("mode: got %v want 0644", info.Mode().Perm())
	}
}

func TestStatExecutable(t *testing.T) {
	v := newVFS(t, "tip")

	info, err := v.Stat("bin/run")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0755 {
		t.Errorf("mode: got %v want 0755", info.Mode().Perm())
	}
}

func TestStatDir(t *testing.T) {
	v := newVFS(t, "tip")

	info, err := v.Stat("src")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !info.IsDir() {
		t.Error("src not flagged dir")
	}
}

func TestOpenNotExist(t *testing.T) {
	v := newVFS(t, "tip")

	_, err := v.Open("does/not/exist")
	if err == nil {
		t.Fatal("expected error opening missing path")
	}
	var pathErr *fs.PathError
	if err != nil && !errorsAsPathErr(err, &pathErr) {
		t.Errorf("expected *fs.PathError, got %T: %v", err, err)
	}
}

// TestFSConformance runs the standard io/fs test suite against the VFS.
// fstest.TestFS verifies that ReadDir, Stat, Open, Read, Close and
// fs.Walk all behave consistently and match each other's view of the tree.
func TestFSConformance(t *testing.T) {
	v := newVFS(t, "tip")

	expected := []string{
		"README.md",
		"src/main.go",
		"src/util/helper.go",
		"docs/intro.md",
		"bin/run",
	}
	if err := fstest.TestFS(v, expected...); err != nil {
		t.Fatal(err)
	}
}

// errorsAsPathErr is a tiny helper to keep imports tidy.
func errorsAsPathErr(err error, target **fs.PathError) bool {
	for err != nil {
		if pe, ok := err.(*fs.PathError); ok {
			*target = pe
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
