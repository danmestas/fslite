package vfs_test

import (
	"os"
	"testing"
)

// TestCreateEmptyFilePersists: a file opened with O_CREATE and closed without
// a single Write must exist afterwards.
//
// WebDAV depends on this. RFC 4918 §9.10.4 requires LOCK on an unmapped URL to
// create an empty locked resource, and x/net/webdav implements that by calling
// OpenFile(O_CREATE|O_TRUNC) and immediately closing. When the empty file
// doesn't stick, the LOCK still answers 201, the client believes the resource
// exists, and the follow-up MOVE fails with "file does not exist" — which is
// how a Linux davfs2 atomic save loses an edit.
func TestCreateEmptyFilePersists(t *testing.T) {
	_, v := newWritableVFS(t)

	f, err := v.OpenFile("empty.txt", os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0666)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	st, err := v.Stat("empty.txt")
	if err != nil {
		t.Fatalf("Stat after creating an empty file: %v", err)
	}
	if st.Size() != 0 {
		t.Errorf("size: got %d, want 0", st.Size())
	}
	if got := readAll(t, v, "empty.txt"); len(got) != 0 {
		t.Errorf("content: got %q, want empty", got)
	}
}

// TestTruncateWithoutWritePersists: `: > file` — open an existing file with
// O_TRUNC and close it without writing — must leave the file empty, not
// silently keep the old content.
func TestTruncateWithoutWritePersists(t *testing.T) {
	_, v := newWritableVFS(t)

	f, err := v.OpenFile("README.md", os.O_WRONLY|os.O_TRUNC, 0666)
	if err != nil {
		t.Fatalf("OpenFile O_TRUNC: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if got := readAll(t, v, "README.md"); len(got) != 0 {
		t.Errorf("content after truncate: got %q, want empty", got)
	}
}

// TestCreatedEmptyFileSurvivesCommit checks the empty file reaches Fossil,
// rather than existing only in the overlay.
func TestCreatedEmptyFileSurvivesCommit(t *testing.T) {
	_, v := newWritableVFS(t)

	f, err := v.OpenFile("placeholder", os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0666)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	f.Close()

	if _, err := v.Commit("add an empty placeholder"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if _, err := v.Stat("placeholder"); err != nil {
		t.Errorf("empty file missing after commit: %v", err)
	}
}
