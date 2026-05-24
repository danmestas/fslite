package vfs_test

import (
	"testing"

	"github.com/nats-io/nats.go"

	"github.com/danmestas/libfossil"

	"github.com/danmestas/fslite/vfs"
)

// TestNoNATSSkipsSyncEvenIfConfigured: NoNATS=true bypasses attachSync
// even when SyncConfig.NATS is non-nil. The VFS comes up local-only
// and v.Sync() returns nil.
func TestNoNATSSkipsSyncEvenIfConfigured(t *testing.T) {
	natsURL := startNATS(t)
	nc, err := nats.Connect(natsURL)
	if err != nil {
		t.Fatalf("nats.Connect: %v", err)
	}
	defer nc.Close()

	repoPath := seedRepoWithCode(t, randomProjectCode(t),
		[]libfossil.FileToCommit{{Name: "README.md", Content: []byte("seed\n")}})

	v, err := vfs.New(vfs.Config{
		RepoPath: repoPath, Version: "tip", EnableWrites: true,
		NoNATS: true, // wins over SyncConfig
		Sync: vfs.SyncConfig{
			NATS: nc, ProjectCode: "ignored", AgentID: "ignored",
		},
	})
	if err != nil {
		t.Fatalf("vfs.New: %v", err)
	}
	defer v.Close()

	if v.Sync() != nil {
		t.Error("Sync runner attached despite NoNATS=true")
	}
	if v.WebDAVHandler() == nil {
		t.Error("WebDAVHandler nil")
	}
}

// TestNoNATSWithoutSyncConfigStillWorks: NoNATS=true with no SyncConfig
// at all is just local-only mode (the most common case).
func TestNoNATSWithoutSyncConfigStillWorks(t *testing.T) {
	repoPath, v0 := newWritableVFS(t)
	v0.Close()

	v, err := vfs.New(vfs.Config{
		RepoPath: repoPath, Version: "tip", EnableWrites: true,
		NoNATS: true,
	})
	if err != nil {
		t.Fatalf("vfs.New: %v", err)
	}
	defer v.Close()

	if v.Sync() != nil {
		t.Error("Sync runner attached with NoNATS+empty SyncConfig")
	}
}
