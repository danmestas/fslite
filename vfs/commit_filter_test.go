package vfs_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/danmestas/go-libfossil"

	"github.com/danmestas/fslite/vfs"
)

// TestCommitFiltersMacOSMetadata: the kernel WebDAV client writes
// AppleDouble sidecars (._foo) and Finder drops .DS_Store. Default
// behaviour is to strip these at commit-drain so the Fossil manifest
// only contains real user files.
func TestCommitFiltersMacOSMetadata(t *testing.T) {
	repoPath, v := newWritableVFS(t)

	// Real user file + macOS pollution alongside it.
	writeViaOpenFile(t, v, "doc.txt", []byte("real content\n"))
	writeViaOpenFile(t, v, "._doc.txt", []byte("AppleDouble junk"))
	writeViaOpenFile(t, v, ".DS_Store", []byte("finder view state"))

	if _, err := v.Commit("with sidecars"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	v.Close()

	v2, err := vfs.New(vfs.Config{RepoPath: repoPath, Version: "tip"})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer v2.Close()

	if got := readAll(t, v2, "doc.txt"); string(got) != "real content\n" {
		t.Errorf("doc.txt: %q", got)
	}
	if _, err := v2.Stat("._doc.txt"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("sidecar leaked into manifest: %v", err)
	}
	if _, err := v2.Stat(".DS_Store"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf(".DS_Store leaked into manifest: %v", err)
	}
}

// TestCommitDisabledIgnoreKeepsAll: IgnoreGlob="-" disables filtering;
// sidecars survive the commit.
func TestCommitDisabledIgnoreKeepsAll(t *testing.T) {
	dir := t.TempDir()
	repoPath := filepath.Join(dir, "test.fossil")
	r, _ := libfossil.Create(repoPath, libfossil.CreateOpts{User: "test"})
	_, _, _ = r.Commit(libfossil.CommitOpts{
		Files:   []libfossil.FileToCommit{{Name: "README.md", Content: []byte("seed\n")}},
		Comment: "seed", User: "test", Time: time.Now().UTC(),
	})
	r.Close()

	v, err := vfs.New(vfs.Config{
		RepoPath: repoPath, Version: "tip", EnableWrites: true,
		IgnoreGlob: "-",
	})
	if err != nil {
		t.Fatalf("vfs.New: %v", err)
	}
	defer v.Close()

	writeViaOpenFile(t, v, "._kept.txt", []byte("preserved"))

	if _, err := v.Commit("keep sidecars"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	v.Close()

	v2, _ := vfs.New(vfs.Config{RepoPath: repoPath, Version: "tip"})
	defer v2.Close()
	if _, err := v2.Stat("._kept.txt"); err != nil {
		t.Errorf("opt-out (IgnoreGlob=-) failed: ._kept.txt absent post-commit: %v", err)
	}
}

// TestCommitConfigTablePersistsIgnore: SetIgnoreGlob writes to the
// repo's vfs-ignore-glob key; the setting takes effect immediately
// AND survives a Close+New cycle on the same repo.
func TestCommitConfigTablePersistsIgnore(t *testing.T) {
	repoPath, v := newWritableVFS(t)

	// Default: .DS_Store would be stripped. Override with a config
	// glob that DOESN'T list .DS_Store → it should now survive a commit.
	if err := v.SetIgnoreGlob("*.tmp,build/*"); err != nil {
		t.Fatalf("SetIgnoreGlob: %v", err)
	}
	source, glob := v.IgnoreGlob()
	if source != "config" || glob != "*.tmp,build/*" {
		t.Errorf("after SetIgnoreGlob: source=%q glob=%q", source, glob)
	}

	writeViaOpenFile(t, v, ".DS_Store", []byte("now allowed"))
	writeViaOpenFile(t, v, "drop.tmp", []byte("filtered"))
	if _, err := v.Commit("config-table ignore"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	v.Close()

	// Reopen: setting was persisted to fossil config table.
	v2, _ := vfs.New(vfs.Config{RepoPath: repoPath, Version: "tip"})
	defer v2.Close()
	if _, err := v2.Stat(".DS_Store"); err != nil {
		t.Errorf(".DS_Store should have survived custom-config commit: %v", err)
	}
	if _, err := v2.Stat("drop.tmp"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("drop.tmp should have been filtered by config glob: %v", err)
	}
	source2, glob2 := v2.IgnoreGlob()
	if source2 != "config" || glob2 != "*.tmp,build/*" {
		t.Errorf("post-reopen IgnoreGlob: source=%q glob=%q", source2, glob2)
	}
}

// TestCommitErrNothingToCommitForOnlySidecars: when KeepMacOSMetadata
// is false (default), an overlay containing only macOS metadata yields
// ErrNothingToCommit rather than creating an empty check-in.
func TestCommitErrNothingToCommitForOnlySidecars(t *testing.T) {
	_, v := newWritableVFS(t)

	writeViaOpenFile(t, v, "._junk.txt", []byte("junk"))
	writeViaOpenFile(t, v, ".DS_Store", []byte("finder"))

	_, err := v.Commit("only sidecars")
	if !errors.Is(err, vfs.ErrNothingToCommit) {
		t.Errorf("commit of only-sidecars: got err=%v want ErrNothingToCommit", err)
	}
}

// TestCommitErrNothingToCommitForEmptyOverlay covers the simpler case:
// no writes at all → ErrNothingToCommit (was bare errors.New before).
func TestCommitErrNothingToCommitForEmptyOverlay(t *testing.T) {
	_, v := newWritableVFS(t)

	_, err := v.Commit("nothing")
	if !errors.Is(err, vfs.ErrNothingToCommit) {
		t.Errorf("empty commit: got err=%v want ErrNothingToCommit", err)
	}
}

// Silence the unused-import lint when only one of these test files is built.
var _ = os.O_RDONLY
