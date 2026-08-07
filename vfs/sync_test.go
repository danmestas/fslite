package vfs_test

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"


	"github.com/danmestas/go-libfossil"

	"github.com/danmestas/fslite/vfs"
)

// startNATS spins up an in-process NATS server with JetStream enabled
// on a random localhost port. JetStream is required for the cross-agent
// WebDAV lock system; the sync transport doesn't need it but it's free
// to leave on for all tests.
func startNATS(t *testing.T) string {
	t.Helper()
	opts := &natsserver.Options{
		Host:      "127.0.0.1",
		Port:      -1, // random
		JetStream: true,
		StoreDir:  t.TempDir(),
	}
	s, err := natsserver.NewServer(opts)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	go s.Start()
	t.Cleanup(s.Shutdown)
	if !s.ReadyForConnections(2 * time.Second) {
		t.Fatal("nats server not ready")
	}
	return s.ClientURL()
}

func randomProjectCode(t *testing.T) string {
	t.Helper()
	var b [20]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return hex.EncodeToString(b[:])
}

// seedRepoWithCode creates a Fossil repo with a known ProjectCode and a
// single initial commit. Returns the on-disk repo path.
func seedRepoWithCode(t *testing.T, projectCode string, seed []libfossil.FileToCommit) string {
	t.Helper()
	dir := t.TempDir()
	repoPath := filepath.Join(dir, "test.fossil")

	r, err := libfossil.Create(repoPath, libfossil.CreateOpts{
		User:        "test",
		ProjectCode: projectCode,
	})
	if err != nil {
		t.Fatalf("Create %s: %v", repoPath, err)
	}
	if _, _, err := r.Commit(libfossil.CommitOpts{
		Files:   seed,
		Comment: "seed", User: "test", Time: time.Now().UTC(),
	}); err != nil {
		r.Close()
		t.Fatalf("seed Commit: %v", err)
	}
	r.Close()
	return repoPath
}

// twoSharedRepos creates a seeded source repo and a byte-identical peer
// (filesystem-copy so both start with the same UUIDs, not just the same
// project code). This is the prerequisite for libfossil sync to converge
// — peers need a shared history to know what's "new" between them.
func twoSharedRepos(t *testing.T, projectCode string, seed []libfossil.FileToCommit) (string, string) {
	t.Helper()
	src := seedRepoWithCode(t, projectCode, seed)
	peerDir := t.TempDir()
	peer := filepath.Join(peerDir, "peer.fossil")

	srcBytes, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read source repo: %v", err)
	}
	if err := os.WriteFile(peer, srcBytes, 0644); err != nil {
		t.Fatalf("write peer repo: %v", err)
	}
	return src, peer
}

// pollUntil polls fn until it returns nil or the deadline elapses.
func pollUntil(t *testing.T, deadline time.Duration, fn func() error) {
	t.Helper()
	end := time.Now().Add(deadline)
	var last error
	for time.Now().Before(end) {
		if err := fn(); err == nil {
			return
		} else {
			last = err
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pollUntil timed out: %v", last)
}

// TestSyncCommitNotificationPropagates is the e2e: agent A commits, agent B
// receives the notification, pulls via NATS-mediated xfer, and sees the
// committed file via its own VFS API.
func TestSyncCommitNotificationPropagates(t *testing.T) {
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
	defer ncA.Close()
	ncB, err := nats.Connect(natsURL)
	if err != nil {
		t.Fatalf("nats B: %v", err)
	}
	defer ncB.Close()

	vA, err := vfs.New(vfs.Config{
		RepoPath: repoA, Version: "tip", EnableWrites: true, User: "agentA",
		Sync: vfs.SyncConfig{
			NATS: ncA, ProjectCode: projectCode, AgentID: "agentA",
		},
	})
	if err != nil {
		t.Fatalf("vfs.New A: %v", err)
	}
	defer vA.Close()

	vB, err := vfs.New(vfs.Config{
		RepoPath: repoB, Version: "tip", EnableWrites: true, User: "agentB",
		Sync: vfs.SyncConfig{
			NATS: ncB, ProjectCode: projectCode, AgentID: "agentB",
		},
	})
	if err != nil {
		t.Fatalf("vfs.New B: %v", err)
	}
	defer vB.Close()

	// Give subscriptions a moment to register with the server.
	time.Sleep(50 * time.Millisecond)

	// Agent A writes + commits.
	writeViaOpenFile(t, vA, "src/new.go", []byte("package new\n"))
	if _, err := vA.Commit("from A"); err != nil {
		t.Fatalf("Commit A: %v", err)
	}

	// Agent B should converge — its VFS should see src/new.go after the
	// notification + pull cycle completes.
	pollUntil(t, 5*time.Second, func() error {
		f, err := vB.Open("src/new.go")
		if err != nil {
			return err
		}
		defer f.Close()
		data, err := io.ReadAll(f)
		if err != nil {
			return err
		}
		if string(data) != "package new\n" {
			return errors.New("content mismatch")
		}
		return nil
	})
}

// TestSyncManualPullWithoutNotification verifies the explicit PullFromPeer
// path: an agent can pull from a named peer without waiting for a
// notification. Useful for catching up after reconnect.
func TestSyncManualPullWithoutNotification(t *testing.T) {
	natsURL := startNATS(t)
	projectCode := randomProjectCode(t)

	seed := []libfossil.FileToCommit{
		{Name: "README.md", Content: []byte("seed\n")},
	}
	repoA, repoB := twoSharedRepos(t, projectCode, seed)

	ncA, _ := nats.Connect(natsURL)
	defer ncA.Close()
	ncB, _ := nats.Connect(natsURL)
	defer ncB.Close()

	// A is the "server" — it'll respond to sync requests.
	vA, err := vfs.New(vfs.Config{
		RepoPath: repoA, Version: "tip", EnableWrites: true, User: "agentA",
		Sync: vfs.SyncConfig{
			NATS: ncA, ProjectCode: projectCode, AgentID: "agentA",
		},
	})
	if err != nil {
		t.Fatalf("vfs.New A: %v", err)
	}
	defer vA.Close()

	// Give A's subscriptions time to register before B tries to pull.
	time.Sleep(50 * time.Millisecond)

	// A commits something privately (no notification yet from B's POV,
	// since we're going to disable that path by closing B's NATS before
	// creating it... actually simpler: create B *after* A commits).
	writeViaOpenFile(t, vA, "private.txt", []byte("private\n"))
	if _, err := vA.Commit("private"); err != nil {
		t.Fatalf("Commit A: %v", err)
	}

	// Now bring up B and trigger a manual pull.
	vB, err := vfs.New(vfs.Config{
		RepoPath: repoB, Version: "tip", EnableWrites: true, User: "agentB",
		Sync: vfs.SyncConfig{
			NATS: ncB, ProjectCode: projectCode, AgentID: "agentB",
		},
	})
	if err != nil {
		t.Fatalf("vfs.New B: %v", err)
	}
	defer vB.Close()

	if err := vB.Sync().PullFromPeer("agentA"); err != nil {
		t.Fatalf("manual PullFromPeer: %v", err)
	}

	// B should now see the file that A committed.
	f, err := vB.Open("private.txt")
	if err != nil {
		t.Fatalf("Open after manual pull: %v", err)
	}
	defer f.Close()
	data, _ := io.ReadAll(f)
	if string(data) != "private\n" {
		t.Errorf("content: got %q want %q", data, "private\n")
	}
}

// TestSyncDisabledNoSubscriptions is a sanity check that a VFS without
// SyncConfig doesn't talk to NATS at all.
func TestSyncDisabledNoSubscriptions(t *testing.T) {
	repoPath, v := newWritableVFS(t)
	_ = repoPath

	if v.Sync() != nil {
		t.Error("expected nil sync runner with no SyncConfig")
	}
	if _, err := v.Stat("README.md"); err != nil {
		t.Errorf("read should still work without sync: %v", err)
	}
}

func TestSyncRejectsBadConfig(t *testing.T) {
	natsURL := startNATS(t)
	nc, _ := nats.Connect(natsURL)
	defer nc.Close()

	repoPath := seedRepoWithCode(t, randomProjectCode(t),
		[]libfossil.FileToCommit{{Name: "x", Content: []byte("x")}})

	// Missing ProjectCode.
	_, err := vfs.New(vfs.Config{
		RepoPath: repoPath, Version: "tip", EnableWrites: true,
		Sync: vfs.SyncConfig{NATS: nc, AgentID: "a"},
	})
	if err == nil {
		t.Error("expected error: missing ProjectCode")
	}

	// Missing AgentID.
	_, err = vfs.New(vfs.Config{
		RepoPath: repoPath, Version: "tip", EnableWrites: true,
		Sync: vfs.SyncConfig{NATS: nc, ProjectCode: "x"},
	})
	if err == nil {
		t.Error("expected error: missing AgentID")
	}
}

// staticPoll silences linter unused warnings on imports the test uses
// conditionally.
var _ = fs.ErrNotExist
