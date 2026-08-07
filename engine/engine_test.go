package engine_test

import (
	"path/filepath"
	"testing"
	"time"


	"github.com/danmestas/go-libfossil"

	"github.com/danmestas/fslite/engine"
)

// createTestRepo creates a Fossil repo with one checked-in file and returns the repo path.
func createTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	repoPath := filepath.Join(dir, "test.fossil")

	r, err := libfossil.Create(repoPath, libfossil.CreateOpts{User: "test"})
	if err != nil {
		t.Fatalf("libfossil.Create: %v", err)
	}

	_, _, err = r.Commit(libfossil.CommitOpts{
		Files: []libfossil.FileToCommit{
			{Name: "hello.txt", Content: []byte("hello world\n")},
		},
		Comment: "initial",
		User:    "test",
		Time:    time.Now().UTC(),
	})
	if err != nil {
		r.Close()
		t.Fatalf("Commit: %v", err)
	}
	r.Close()
	return repoPath
}

func TestOpenAndListFiles(t *testing.T) {
	repoPath := createTestRepo(t)

	eng, err := engine.Open(repoPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer eng.Close()

	files, err := eng.ListFiles("tip")
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if files[0].Name != "hello.txt" {
		t.Errorf("expected name hello.txt, got %s", files[0].Name)
	}
	if files[0].UUID == "" {
		t.Error("expected non-empty UUID")
	}
}

func TestCheckinAddsFile(t *testing.T) {
	repoPath := createTestRepo(t)

	eng, err := engine.Open(repoPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer eng.Close()

	err = eng.Checkin([]engine.WriteFile{
		{Path: "hello.txt", Content: []byte("hello world\n")},
		{Path: "new.txt", Content: []byte("new file\n")},
	}, "add new file")
	if err != nil {
		t.Fatalf("Checkin: %v", err)
	}

	files, err := eng.ListFiles("tip")
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}

	var newFile engine.FileEntry
	for _, f := range files {
		if f.Name == "new.txt" {
			newFile = f
			break
		}
	}
	if newFile.UUID == "" {
		t.Fatal("new.txt not found in listing")
	}
}

func TestStat(t *testing.T) {
	repoPath := createTestRepo(t)

	eng, err := engine.Open(repoPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer eng.Close()

	info, err := eng.Stat("tip")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.CheckinUUID == "" {
		t.Error("expected non-empty CheckinUUID")
	}
	if info.FileCount != 1 {
		t.Errorf("expected FileCount 1, got %d", info.FileCount)
	}
}
