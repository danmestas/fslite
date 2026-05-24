package vfs_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/danmestas/libfossil"

	"github.com/danmestas/fslite/vfs"
)

// twoSyncedAgents brings up two VFS instances sharing a project code
// over the same NATS bus and returns them ready for tests.
func twoSyncedAgents(t *testing.T) (*vfs.VFS, *vfs.VFS) {
	t.Helper()
	natsURL := startNATS(t)
	projectCode := randomProjectCode(t)

	seed := []libfossil.FileToCommit{
		{Name: "README.md", Content: []byte("seed\n")},
	}
	repoA, repoB := twoSharedRepos(t, projectCode, seed)

	ncA, err := nats.Connect(natsURL)
	if err != nil {
		t.Fatalf("nats A: %v", err)
	}
	t.Cleanup(ncA.Close)
	ncB, err := nats.Connect(natsURL)
	if err != nil {
		t.Fatalf("nats B: %v", err)
	}
	t.Cleanup(ncB.Close)

	vA, err := vfs.New(vfs.Config{
		RepoPath: repoA, Version: "tip", EnableWrites: true, User: "agentA",
		Sync: vfs.SyncConfig{NATS: ncA, ProjectCode: projectCode, AgentID: "agentA"},
	})
	if err != nil {
		t.Fatalf("vfs.New A: %v", err)
	}
	t.Cleanup(func() { vA.Close() })

	vB, err := vfs.New(vfs.Config{
		RepoPath: repoB, Version: "tip", EnableWrites: true, User: "agentB",
		Sync: vfs.SyncConfig{NATS: ncB, ProjectCode: projectCode, AgentID: "agentB"},
	})
	if err != nil {
		t.Fatalf("vfs.New B: %v", err)
	}
	t.Cleanup(func() { vB.Close() })

	return vA, vB
}

// lockRequest formats a WebDAV LOCK XML body. Most clients send something
// like this verbatim.
func lockRequestBody(owner, root string) string {
	return strings.Join([]string{
		`<?xml version="1.0" encoding="utf-8"?>`,
		`<D:lockinfo xmlns:D="DAV:">`,
		`<D:lockscope><D:exclusive/></D:lockscope>`,
		`<D:locktype><D:write/></D:locktype>`,
		`<D:owner><D:href>` + owner + `</D:href></D:owner>`,
		`</D:lockinfo>`,
	}, "")
}

// startWebDAVTestServer returns a base URL for the agent's WebDAV handler
// served via httptest. Cleanup is registered on t.
func startWebDAVTestServer(t *testing.T, v *vfs.VFS) string {
	t.Helper()
	srv := httptest.NewServer(v.WebDAVHandler())
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestLockAcquiredOnOneAgentBlocksOther is the headline cross-agent test:
// agent A LOCKs /shared.txt; agent B's LOCK on the same path is rejected
// with 423 Locked. This requires the JetStream-KV-backed lock system —
// without it (or with MemLS), B would happily grant its own lock.
func TestLockAcquiredOnOneAgentBlocksOther(t *testing.T) {
	vA, vB := twoSyncedAgents(t)

	urlA := startWebDAVTestServer(t, vA)
	urlB := startWebDAVTestServer(t, vB)

	// Both agents need to know about the file. We'll write it on A and
	// commit so B can pull it.
	writeViaOpenFile(t, vA, "shared.txt", []byte("shared\n"))
	if _, err := vA.Commit("seed shared"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	body := lockRequestBody("agentA", "/shared.txt")
	lockA := httpDo(t, "LOCK", urlA+"/shared.txt", []byte(body), map[string]string{
		"Content-Type": "application/xml",
		"Depth":        "0",
		"Timeout":      "Second-300",
	})
	defer lockA.Body.Close()
	if lockA.StatusCode != http.StatusOK {
		t.Fatalf("LOCK on A: got %d", lockA.StatusCode)
	}

	// Same path, second agent — must be rejected.
	body2 := lockRequestBody("agentB", "/shared.txt")
	lockB := httpDo(t, "LOCK", urlB+"/shared.txt", []byte(body2), map[string]string{
		"Content-Type": "application/xml",
		"Depth":        "0",
		"Timeout":      "Second-300",
	})
	defer lockB.Body.Close()
	if lockB.StatusCode != http.StatusLocked {
		t.Errorf("expected 423 Locked from agent B, got %d", lockB.StatusCode)
	}
}

// TestUnlockOnOneAgentClearsTheOther: A locks, A unlocks, B can now lock.
func TestUnlockOnOneAgentClearsTheOther(t *testing.T) {
	vA, vB := twoSyncedAgents(t)
	urlA := startWebDAVTestServer(t, vA)
	urlB := startWebDAVTestServer(t, vB)

	writeViaOpenFile(t, vA, "shared.txt", []byte("shared\n"))
	if _, err := vA.Commit("seed"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	// A locks
	lockA := httpDo(t, "LOCK", urlA+"/shared.txt",
		[]byte(lockRequestBody("agentA", "/shared.txt")),
		map[string]string{
			"Content-Type": "application/xml",
			"Depth":        "0",
			"Timeout":      "Second-300",
		})
	defer lockA.Body.Close()
	if lockA.StatusCode != http.StatusOK {
		t.Fatalf("LOCK on A: %d", lockA.StatusCode)
	}
	token := lockA.Header.Get("Lock-Token")
	if token == "" {
		t.Fatal("no Lock-Token header in LOCK response")
	}

	// A unlocks
	unlockA := httpDo(t, "UNLOCK", urlA+"/shared.txt", nil, map[string]string{
		"Lock-Token": token,
	})
	defer unlockA.Body.Close()
	if unlockA.StatusCode != http.StatusNoContent {
		t.Fatalf("UNLOCK on A: %d", unlockA.StatusCode)
	}

	// B should now be able to lock.
	lockB := httpDo(t, "LOCK", urlB+"/shared.txt",
		[]byte(lockRequestBody("agentB", "/shared.txt")),
		map[string]string{
			"Content-Type": "application/xml",
			"Depth":        "0",
			"Timeout":      "Second-300",
		})
	defer lockB.Body.Close()
	if lockB.StatusCode != http.StatusOK {
		t.Errorf("LOCK on B after unlock: got %d", lockB.StatusCode)
	}
}

// TestLocksDifferentPathsCoexist: A locks /a.txt; B locks /b.txt; both succeed.
func TestLocksDifferentPathsCoexist(t *testing.T) {
	vA, vB := twoSyncedAgents(t)
	urlA := startWebDAVTestServer(t, vA)
	urlB := startWebDAVTestServer(t, vB)

	writeViaOpenFile(t, vA, "a.txt", []byte("a\n"))
	writeViaOpenFile(t, vA, "b.txt", []byte("b\n"))
	if _, err := vA.Commit("seed"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	for _, c := range []struct {
		url, path string
	}{
		{urlA, "/a.txt"},
		{urlB, "/b.txt"},
	} {
		resp := httpDo(t, "LOCK", c.url+c.path,
			[]byte(lockRequestBody("test", c.path)),
			map[string]string{
				"Content-Type": "application/xml",
				"Depth":        "0",
				"Timeout":      "Second-300",
			})
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("LOCK %s on %s: got %d", c.path, c.url, resp.StatusCode)
		}
	}
}

