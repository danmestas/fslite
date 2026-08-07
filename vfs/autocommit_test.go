package vfs_test

import (
	"errors"
	"io/fs"
	"path/filepath"
	"testing"
	"time"

	"github.com/danmestas/go-libfossil"

	"github.com/danmestas/fslite/vfs"
)

// TestAutoCommitDebounces opens a VFS with a short auto-commit window,
// writes a file, waits, and verifies the file ended up in the manifest
// (not still in the overlay) without an explicit Commit call.
func TestAutoCommitDebounces(t *testing.T) {
	repoPath := newSeededRepoForAutoCommit(t)

	v, err := vfs.New(vfs.Config{
		RepoPath:     repoPath,
		Version:      "tip",
		EnableWrites: true,
		User:         "test",
		AutoCommit:   150 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("vfs.New: %v", err)
	}
	defer v.Close()

	writeViaOpenFile(t, v, "src/added.go", []byte("package main\n"))

	// Before the debounce fires, the file is overlay-visible only.
	// After it fires, the overlay is drained and the file is in the
	// manifest. We poll for up to 1s to absorb scheduler jitter.
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		v2, err := vfs.New(vfs.Config{RepoPath: repoPath, Version: "tip"})
		if err != nil {
			t.Fatal(err)
		}
		_, statErr := v2.Stat("src/added.go")
		v2.Close()
		if statErr == nil {
			return // committed!
		}
		if !errors.Is(statErr, fs.ErrNotExist) {
			t.Fatalf("unexpected Stat error: %v", statErr)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("auto-commit did not fire within 1s of debounce window elapsing")
}

// TestAutoCommitResetsOnBurst confirms that consecutive writes reset
// the timer — the commit fires AFTER the last write + window, not
// AFTER the first write + window.
func TestAutoCommitResetsOnBurst(t *testing.T) {
	repoPath := newSeededRepoForAutoCommit(t)

	// Generous relative to scheduler jitter: each write has to land within
	// `window` of the previous one for the burst to stay a burst, and 750ms
	// of slack is a lot more forgiving than the 120ms this test used to run
	// on. Wall-clock cost is a couple of seconds.
	const (
		window = 1 * time.Second
		gap    = 250 * time.Millisecond
	)

	v, err := vfs.New(vfs.Config{
		RepoPath:     repoPath,
		Version:      "tip",
		EnableWrites: true,
		User:         "test",
		AutoCommit:   window,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()

	// Burst of 4 writes, one gap apart. Each resets the timer, so nothing
	// should have committed by the end of the burst.
	//
	// The assertion only holds while every gap stays under the window —
	// that is the premise of the test, not the thing under test. A loaded
	// machine (Windows CI is the usual offender) can stall a write past the
	// window, at which point a commit is correct behaviour rather than a
	// bug, so measure the gaps and skip instead of reporting a false
	// failure.
	var maxGap time.Duration
	for i := 0; i < 4; i++ {
		start := time.Now()
		writeViaOpenFile(t, v, "burst-"+timestamp(i), []byte("x"))
		time.Sleep(gap)
		if elapsed := time.Since(start); elapsed > maxGap {
			maxGap = elapsed
		}
	}

	// Right after the burst: nothing in the manifest yet (the timer
	// just got reset by the 4th write).
	v2, _ := vfs.New(vfs.Config{RepoPath: repoPath, Version: "tip"})
	_, statErr := v2.Stat("burst-0")
	v2.Close()
	if statErr == nil {
		if maxGap >= window {
			t.Skipf("machine stalled mid-burst: slowest write+gap was %v, debounce window is %v; "+
				"a commit during the burst is correct under those timings", maxGap, window)
		}
		t.Error("commit fired during burst; debounce reset not working")
	}

	// Well past the window → committed.
	time.Sleep(window + window/2)
	v3, _ := vfs.New(vfs.Config{RepoPath: repoPath, Version: "tip"})
	defer v3.Close()
	if _, err := v3.Stat("burst-0"); err != nil {
		t.Errorf("commit didn't fire after burst quieted: %v", err)
	}
	if _, err := v3.Stat("burst-3"); err != nil {
		t.Errorf("last burst write missing from commit: %v", err)
	}
}

// TestAutoCommitDisabledByDefault: AutoCommit=0 → no goroutine, no
// commits without an explicit call.
func TestAutoCommitDisabledByDefault(t *testing.T) {
	repoPath := newSeededRepoForAutoCommit(t)

	v, err := vfs.New(vfs.Config{
		RepoPath:     repoPath,
		Version:      "tip",
		EnableWrites: true,
		User:         "test",
		// AutoCommit unset → 0 → disabled
	})
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()

	writeViaOpenFile(t, v, "untouched.md", []byte("x"))
	time.Sleep(200 * time.Millisecond)

	v2, _ := vfs.New(vfs.Config{RepoPath: repoPath, Version: "tip"})
	defer v2.Close()
	if _, err := v2.Stat("untouched.md"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("file landed in manifest with AutoCommit=0: %v", err)
	}
}

// newSeededRepoForAutoCommit creates a fresh repo with one seed file
// and returns the path. (Local helper to avoid sharing state with
// other test helpers that create writable VFS instances.)
func newSeededRepoForAutoCommit(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	repoPath := filepath.Join(dir, "test.fossil")
	r, err := libfossil.Create(repoPath, libfossil.CreateOpts{User: "test"})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = r.Commit(libfossil.CommitOpts{
		Files:   []libfossil.FileToCommit{{Name: "README.md", Content: []byte("seed\n")}},
		Comment: "seed", User: "test", Time: time.Now().UTC(),
	})
	r.Close()
	if err != nil {
		t.Fatal(err)
	}
	return repoPath
}

func timestamp(i int) string {
	switch i {
	case 0:
		return "0"
	case 1:
		return "1"
	case 2:
		return "2"
	case 3:
		return "3"
	}
	return ""
}
