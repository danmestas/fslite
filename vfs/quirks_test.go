package vfs_test

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/danmestas/go-libfossil"

	"github.com/danmestas/fslite/vfs"
)

// TestMOVEDefaultsToOverwrite covers the macOS atomic-save flow:
// Cocoa's NSDocument writes a temp file in the same dir and renames it
// onto the target via the kernel WebDAV client, which omits the
// Overwrite header. Per RFC 4918 §9.9.4 the server must default to
// Overwrite: T. The bare upstream webdav.Handler defaults to FALSE,
// which broke every TextEdit/Pages save through the mount. The
// webdavQuirkFixer middleware patches this; this test pins the fix.
func TestMOVEDefaultsToOverwrite(t *testing.T) {
	dir := t.TempDir()
	repoPath := dir + "/test.fossil"
	r, err := libfossil.Create(repoPath, libfossil.CreateOpts{User: "test"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, _, err = r.Commit(libfossil.CommitOpts{
		Files: []libfossil.FileToCommit{
			{Name: "a.txt", Content: []byte("AAA")},
		},
		Comment: "seed", User: "test", Time: time.Now().UTC(),
	})
	if err != nil {
		r.Close()
		t.Fatalf("Commit: %v", err)
	}
	r.Close()

	v, err := vfs.New(vfs.Config{
		RepoPath:     repoPath,
		Version:      "tip",
		EnableWrites: true,
	})
	if err != nil {
		t.Fatalf("vfs.New: %v", err)
	}
	defer v.Close()

	srv := httptest.NewServer(v.WebDAVHandler())
	defer srv.Close()

	// PUT a new file at /b.txt — the "temp" file in the atomic-save dance.
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/b.txt",
		bytes.NewReader([]byte("BBB-new")))
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("PUT /b.txt: %d", resp.StatusCode)
	}

	// MOVE /b.txt → /a.txt WITHOUT an Overwrite header — the macOS kernel
	// pattern. Must succeed per RFC 4918 §9.9.4.
	mvReq, _ := http.NewRequest("MOVE", srv.URL+"/b.txt", nil)
	mvReq.Header.Set("Destination", srv.URL+"/a.txt")
	// Intentionally NO Overwrite header.
	mvResp, _ := http.DefaultClient.Do(mvReq)
	body, _ := io.ReadAll(mvResp.Body)
	mvResp.Body.Close()
	if mvResp.StatusCode >= 300 {
		t.Fatalf("MOVE without Overwrite header: got %d %q (want 2xx)",
			mvResp.StatusCode, body)
	}

	// Verify the target now has the new content and the source is gone.
	getResp, _ := http.Get(srv.URL + "/a.txt")
	got, _ := io.ReadAll(getResp.Body)
	getResp.Body.Close()
	if string(got) != "BBB-new" {
		t.Errorf("/a.txt: got %q want %q", got, "BBB-new")
	}
	srcResp, _ := http.Get(srv.URL + "/b.txt")
	srcResp.Body.Close()
	if srcResp.StatusCode != http.StatusNotFound {
		t.Errorf("/b.txt should be gone after MOVE; status %d", srcResp.StatusCode)
	}
}

// TestMOVEOverwriteFalseStillBlocks confirms the middleware doesn't
// silently override explicit "Overwrite: F" requests.
func TestMOVEOverwriteFalseStillBlocks(t *testing.T) {
	dir := t.TempDir()
	repoPath := dir + "/test.fossil"
	r, _ := libfossil.Create(repoPath, libfossil.CreateOpts{User: "test"})
	_, _, _ = r.Commit(libfossil.CommitOpts{
		Files: []libfossil.FileToCommit{
			{Name: "a.txt", Content: []byte("AAA")},
		},
		Comment: "seed", User: "test", Time: time.Now().UTC(),
	})
	r.Close()

	v, _ := vfs.New(vfs.Config{
		RepoPath: repoPath, Version: "tip", EnableWrites: true,
	})
	defer v.Close()

	srv := httptest.NewServer(v.WebDAVHandler())
	defer srv.Close()

	put, _ := http.NewRequest(http.MethodPut, srv.URL+"/b.txt",
		bytes.NewReader([]byte("BBB")))
	r1, _ := http.DefaultClient.Do(put)
	r1.Body.Close()

	mv, _ := http.NewRequest("MOVE", srv.URL+"/b.txt", nil)
	mv.Header.Set("Destination", srv.URL+"/a.txt")
	mv.Header.Set("Overwrite", "F")
	r2, _ := http.DefaultClient.Do(mv)
	r2.Body.Close()
	if r2.StatusCode != http.StatusPreconditionFailed {
		t.Errorf("MOVE with Overwrite: F over existing target: got %d want 412", r2.StatusCode)
	}
}
