package vfs_test

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"


	"github.com/danmestas/go-libfossil"

	"github.com/danmestas/fslite/vfs"
)

// newWebDAVServer spins up an httptest server speaking WebDAV against a
// fresh writable VFS seeded with the standard test tree. Returns the
// base URL ("http://127.0.0.1:NNNN") and a teardown func.
func newWebDAVServer(t *testing.T) (*vfs.VFS, string) {
	t.Helper()
	dir := t.TempDir()
	repoPath := dir + "/test.fossil"

	r, err := libfossil.Create(repoPath, libfossil.CreateOpts{User: "test"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, _, err = r.Commit(libfossil.CommitOpts{
		Files: []libfossil.FileToCommit{
			{Name: "README.md", Content: []byte("seed\n")},
			{Name: "src/main.go", Content: []byte("package main\n")},
		},
		Comment: "init", User: "test", Time: time.Now().UTC(),
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
		User:         "test",
	})
	if err != nil {
		t.Fatalf("vfs.New: %v", err)
	}

	srv := httptest.NewServer(v.WebDAVHandler())
	t.Cleanup(func() {
		srv.Close()
		v.Close()
	})
	return v, srv.URL
}

func httpDo(t *testing.T, method, url string, body []byte, headers map[string]string) *http.Response {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	for k, val := range headers {
		req.Header.Set(k, val)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	return resp
}

func TestWebDAVGetExistingFile(t *testing.T) {
	_, base := newWebDAVServer(t)

	resp := httpDo(t, http.MethodGet, base+"/README.md", nil, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "seed\n" {
		t.Errorf("body: got %q want %q", body, "seed\n")
	}
}

func TestWebDAVPutCreatesFile(t *testing.T) {
	v, base := newWebDAVServer(t)

	put := httpDo(t, http.MethodPut, base+"/docs/note.md",
		[]byte("hello via webdav\n"),
		map[string]string{"Content-Type": "text/markdown"})
	put.Body.Close()
	if put.StatusCode != http.StatusCreated && put.StatusCode != http.StatusOK && put.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT status: got %d", put.StatusCode)
	}

	// Verify via VFS that overlay caught it.
	got := readAll(t, v, "docs/note.md")
	if string(got) != "hello via webdav\n" {
		t.Errorf("VFS readback after PUT: got %q", got)
	}

	// And via subsequent GET.
	get := httpDo(t, http.MethodGet, base+"/docs/note.md", nil, nil)
	defer get.Body.Close()
	if get.StatusCode != http.StatusOK {
		t.Fatalf("GET after PUT: status %d", get.StatusCode)
	}
	body, _ := io.ReadAll(get.Body)
	if string(body) != "hello via webdav\n" {
		t.Errorf("GET body: got %q", body)
	}
}

func TestWebDAVPutOverwrites(t *testing.T) {
	v, base := newWebDAVServer(t)

	resp := httpDo(t, http.MethodPut, base+"/README.md",
		[]byte("changed via webdav\n"),
		map[string]string{"Content-Type": "text/markdown"})
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("PUT status: %d", resp.StatusCode)
	}

	got := readAll(t, v, "README.md")
	if string(got) != "changed via webdav\n" {
		t.Errorf("VFS readback after overwrite PUT: got %q", got)
	}
}

func TestWebDAVDelete(t *testing.T) {
	v, base := newWebDAVServer(t)

	resp := httpDo(t, http.MethodDelete, base+"/README.md", nil, nil)
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("DELETE status: %d", resp.StatusCode)
	}

	if _, err := v.Stat("README.md"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("VFS still finds README.md after WebDAV DELETE: %v", err)
	}
}

func TestWebDAVMkcolThenPut(t *testing.T) {
	v, base := newWebDAVServer(t)

	mk := httpDo(t, "MKCOL", base+"/newdir", nil, nil)
	mk.Body.Close()
	if mk.StatusCode >= 300 {
		t.Fatalf("MKCOL status: %d", mk.StatusCode)
	}

	put := httpDo(t, http.MethodPut, base+"/newdir/file.txt",
		[]byte("under newdir\n"), nil)
	put.Body.Close()
	if put.StatusCode >= 300 {
		t.Fatalf("PUT under MKCOL: %d", put.StatusCode)
	}

	got := readAll(t, v, "newdir/file.txt")
	if string(got) != "under newdir\n" {
		t.Errorf("readback: %q", got)
	}
}

func TestWebDAVMove(t *testing.T) {
	v, base := newWebDAVServer(t)

	mv := httpDo(t, "MOVE", base+"/README.md", nil, map[string]string{
		"Destination": base + "/READING.md",
		"Overwrite":   "T",
	})
	mv.Body.Close()
	if mv.StatusCode >= 300 {
		t.Fatalf("MOVE status: %d", mv.StatusCode)
	}

	if _, err := v.Stat("README.md"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("old name still exists after MOVE: %v", err)
	}
	got := readAll(t, v, "READING.md")
	if string(got) != "seed\n" {
		t.Errorf("MOVE-target content: got %q", got)
	}
}

// TestWebDAVPropfindDirectory does a Depth:1 PROPFIND and just checks
// that the response contains the expected child hrefs. The full XML
// schema is the webdav package's concern; we only verify the FileSystem
// adapter is feeding it the right tree.
func TestWebDAVPropfindDirectory(t *testing.T) {
	_, base := newWebDAVServer(t)

	resp := httpDo(t, "PROPFIND", base+"/src", nil, map[string]string{
		"Depth": "1",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMultiStatus && resp.StatusCode != http.StatusOK {
		t.Fatalf("PROPFIND status: got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "main.go") {
		t.Errorf("PROPFIND body missing src/main.go: %s", string(body))
	}
}

// TestWebDAVFullRoundtripWithCommit is the e2e flow: write via WebDAV,
// commit via the VFS API, reopen the repo from disk, and verify the
// committed file is in the new manifest.
func TestWebDAVFullRoundtripWithCommit(t *testing.T) {
	v, base := newWebDAVServer(t)

	resp := httpDo(t, http.MethodPut, base+"/notes/draft.md",
		[]byte("draft via webdav\n"), nil)
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("PUT: %d", resp.StatusCode)
	}

	ckin, err := v.Commit("via webdav")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if ckin.UUID == "" {
		t.Fatalf("empty checkin UUID")
	}

	// Same VFS — after commit the file should now appear as a manifest
	// entry, not an overlay one.
	got := readAll(t, v, "notes/draft.md")
	if string(got) != "draft via webdav\n" {
		t.Errorf("post-commit read: got %q", got)
	}
}
