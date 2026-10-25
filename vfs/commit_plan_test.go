package vfs

import (
	"sort"
	"testing"

	"github.com/danmestas/fslite/engine"
)

func planNames(files []engine.CommitFile) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Name)
	}
	sort.Strings(out)
	return out
}

// An editor's atomic save writes a temp file and renames it over the
// target. The rename tombstones the temp path, which no check-in holds.
// That must not force the full-tree path: the plan carries the changed
// file alone and lets libfossil merge the parent forward.
func TestPlanCommit_TempFileTombstoneIsNotADeletion(t *testing.T) {
	parent := []engine.FileEntry{
		{Name: "README.md", UUID: "aaaa"},
		{Name: "src/main.go", UUID: "bbbb"},
	}
	overlay := []overlayPathEntry{
		{path: "README.md", entry: overlayEntry{content: []byte("saved\n")}},
		{path: ".README.md.swp", entry: overlayEntry{deleted: true}},
	}
	files, partial := planCommit(parent, overlay)
	if partial {
		t.Fatal("a tombstone on an untracked path must not make the manifest partial")
	}
	if got := planNames(files); len(got) != 1 || got[0] != "README.md" {
		t.Errorf("files = %v, want only README.md", got)
	}
	if files[0].UUID != "" || string(files[0].Content) != "saved\n" {
		t.Errorf("changed file must carry content, got UUID=%q content=%q", files[0].UUID, files[0].Content)
	}
}

// Deleting a tracked file spells the manifest out in full, but every
// unchanged file is named by the hash the parent holds, with no content.
func TestPlanCommit_TrackedDeletionCarriesUnchangedByHash(t *testing.T) {
	parent := []engine.FileEntry{
		{Name: "README.md", UUID: "aaaa"},
		{Name: "bin/run", UUID: "cccc", Perm: "x"},
		{Name: "src/main.go", UUID: "bbbb"},
	}
	overlay := []overlayPathEntry{
		{path: "README.md", entry: overlayEntry{deleted: true}},
		{path: "src/main.go", entry: overlayEntry{content: []byte("package main // edited\n")}},
		{path: "src/new.go", entry: overlayEntry{content: []byte("package new\n")}},
		{path: "scratch.tmp", entry: overlayEntry{deleted: true}},
	}
	files, partial := planCommit(parent, overlay)
	if !partial {
		t.Fatal("deleting a tracked file needs the full manifest")
	}
	want := []string{"bin/run", "src/main.go", "src/new.go"}
	if got := planNames(files); !equalStrings(got, want) {
		t.Fatalf("files = %v, want %v", got, want)
	}
	for _, f := range files {
		switch f.Name {
		case "bin/run":
			if f.UUID != "cccc" || f.Content != nil || f.Perm != "x" {
				t.Errorf("unchanged bin/run must be carried by hash with its perm: %+v", f)
			}
		case "src/main.go":
			if f.UUID != "" || string(f.Content) != "package main // edited\n" {
				t.Errorf("edited file must carry content: %+v", f)
			}
		case "src/new.go":
			if f.UUID != "" || string(f.Content) != "package new\n" {
				t.Errorf("added file must carry content: %+v", f)
			}
		}
	}
}

func TestPlanCommit_OnlyStaleTombstonesIsEmpty(t *testing.T) {
	parent := []engine.FileEntry{{Name: "README.md", UUID: "aaaa"}}
	overlay := []overlayPathEntry{{path: "junk.tmp", entry: overlayEntry{deleted: true}}}
	files, partial := planCommit(parent, overlay)
	if partial || len(files) != 0 {
		t.Errorf("stale tombstones alone plan nothing; got %d files, partial=%v", len(files), partial)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
