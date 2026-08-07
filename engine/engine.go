package engine

import (
	"fmt"
	"time"

	"github.com/danmestas/go-libfossil"
)

// FileEntry is metadata for a single file in a checkin.
type FileEntry struct {
	Name string
	UUID string
	Perm string // "" or "x"
}

// WriteFile holds a file to be checked in.
type WriteFile struct {
	Path    string
	Content []byte
}

// RepoInfo holds basic repository statistics.
type RepoInfo struct {
	CheckinUUID string
	FileCount   int64
	Branch      string
}

// Engine provides Fossil repository operations used by the VFS layer.
// Thin wrapper over libfossil's public API.
type Engine struct {
	r    *libfossil.Repo
	path string
}

// Create creates a new Fossil repository at path and returns an open Engine.
func Create(path string) (*Engine, error) {
	r, err := libfossil.Create(path, libfossil.CreateOpts{User: "fslite"})
	if err != nil {
		return nil, fmt.Errorf("create repo: %w", err)
	}
	return &Engine{r: r, path: path}, nil
}

// Open opens an existing Fossil repository.
func Open(path string) (*Engine, error) {
	r, err := libfossil.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open repo: %w", err)
	}
	return &Engine{r: r, path: path}, nil
}

// Close closes the underlying repository.
func (e *Engine) Close() error {
	if e.r != nil {
		return e.r.Close()
	}
	return nil
}

// ResolveVersion resolves a symbolic version ("tip", "trunk", a branch
// name, a UUID, or a UUID prefix) to a numeric checkin RID.
func (e *Engine) ResolveVersion(version string) (int64, error) {
	return e.r.ResolveVersion(version)
}

// ListFiles returns metadata for all files in the given version.
// Version can be "tip", "trunk", a UUID prefix, or empty (defaults to tip).
func (e *Engine) ListFiles(version string) ([]FileEntry, error) {
	rid, err := e.resolveRID(version)
	if err != nil {
		return nil, fmt.Errorf("resolve %q: %w", version, err)
	}
	return e.ListFilesAt(rid)
}

// ListFilesAt returns file metadata for the given checkin RID.
func (e *Engine) ListFilesAt(rid int64) ([]FileEntry, error) {
	libEntries, err := e.r.ListFiles(rid)
	if err != nil {
		return nil, fmt.Errorf("list files: %w", err)
	}

	entries := make([]FileEntry, len(libEntries))
	for i, f := range libEntries {
		entries[i] = FileEntry{
			Name: f.Name,
			UUID: f.UUID,
			Perm: f.Perm,
		}
	}
	return entries, nil
}

// ReadFile returns the content of a single file at the given checkin RID.
// Walks the delta chain internally; no disk checkout is required.
func (e *Engine) ReadFile(rid int64, path string) ([]byte, error) {
	data, err := e.r.ReadFile(rid, path)
	if err != nil {
		return nil, fmt.Errorf("read %q at %d: %w", path, rid, err)
	}
	return data, nil
}

// Repo returns the underlying libfossil repo handle. Exposed for the VFS
// layer's overlay table (a SQLite table living inside the same agent repo
// file). Treat as an escape hatch — prefer the typed methods above.
func (e *Engine) Repo() *libfossil.Repo {
	return e.r
}

// Checkin creates a new commit with the given files and message.
// Preserved for the original FUSE-shape callers; new callers should use
// CommitFiles.
func (e *Engine) Checkin(files []WriteFile, message string) error {
	commitFiles := make([]CommitFile, len(files))
	for i, f := range files {
		commitFiles[i] = CommitFile{Name: f.Path, Content: f.Content}
	}
	parentRID, _ := e.resolveRID("tip")
	_, _, err := e.CommitFiles(commitFiles, message, "fslite", parentRID, false)
	return err
}

// CommitFile describes a single file in a Fossil check-in: name, content,
// and an optional executable-bit marker ("x" or "").
type CommitFile struct {
	Name    string
	Content []byte
	Perm    string
}

// CommitFiles creates a new check-in with the given files. When
// partialManifest is false, files tracked at parentRID but absent from
// files are carried forward automatically (Fossil's "fossil ci" semantics).
// When true, the new manifest contains only the provided files — used by
// the VFS overlay drain when the commit includes deletions.
func (e *Engine) CommitFiles(files []CommitFile, message, user string, parentRID int64, partialManifest bool) (int64, string, error) {
	libFiles := make([]libfossil.FileToCommit, len(files))
	for i, f := range files {
		libFiles[i] = libfossil.FileToCommit{
			Name:    f.Name,
			Content: f.Content,
			Perm:    f.Perm,
		}
	}
	rid, uuid, err := e.r.Commit(libfossil.CommitOpts{
		Files:           libFiles,
		Comment:         message,
		User:            user,
		ParentID:        parentRID,
		Time:            time.Now().UTC(),
		PartialManifest: partialManifest,
	})
	if err != nil {
		return 0, "", fmt.Errorf("commit: %w", err)
	}
	return rid, uuid, nil
}

// Stat returns basic repository information for the given version.
func (e *Engine) Stat(version string) (*RepoInfo, error) {
	rid, err := e.resolveRID(version)
	if err != nil {
		return nil, fmt.Errorf("resolve %q: %w", version, err)
	}

	var uuid string
	if err := e.r.DB().QueryRow("SELECT uuid FROM blob WHERE rid=?", rid).Scan(&uuid); err != nil {
		return nil, fmt.Errorf("get uuid: %w", err)
	}

	files, err := e.r.ListFiles(rid)
	if err != nil {
		return nil, fmt.Errorf("list files: %w", err)
	}

	branch := "trunk"
	var tagName string
	err = e.r.DB().QueryRow(
		"SELECT tag.tagname FROM tagxref JOIN tag ON tag.tagid=tagxref.tagid WHERE tagxref.rid=? AND tag.tagname LIKE 'sym-%' LIMIT 1",
		rid,
	).Scan(&tagName)
	if err == nil && len(tagName) > 4 {
		branch = tagName[4:]
	}

	return &RepoInfo{
		CheckinUUID: uuid,
		FileCount:   int64(len(files)),
		Branch:      branch,
	}, nil
}

// resolveRID resolves a version string to a repository ID.
func (e *Engine) resolveRID(version string) (int64, error) {
	db := e.r.DB()
	var rid int64

	switch version {
	case "", "tip":
		err := db.QueryRow(
			"SELECT objid FROM event WHERE type='ci' ORDER BY mtime DESC LIMIT 1",
		).Scan(&rid)
		if err != nil {
			return 0, fmt.Errorf("no checkins found")
		}
	case "trunk":
		err := db.QueryRow(
			"SELECT tagxref.rid FROM tagxref JOIN tag ON tag.tagid=tagxref.tagid WHERE tag.tagname='sym-trunk' ORDER BY tagxref.mtime DESC LIMIT 1",
		).Scan(&rid)
		if err != nil {
			return e.resolveRID("tip")
		}
	default:
		err := db.QueryRow(
			"SELECT rid FROM blob WHERE uuid LIKE ?||'%'", version,
		).Scan(&rid)
		if err != nil {
			return 0, fmt.Errorf("artifact %q not found", version)
		}
	}
	return rid, nil
}
