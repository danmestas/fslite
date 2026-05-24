package vfs

import (
	"database/sql"
	"errors"
	"fmt"
)

// overlay represents the workspace_overlay SQLite table that lives inside
// the agent's own Fossil repo file. Per spec §5.2 it holds uncommitted
// writes (modified files, additions, deletions/tombstones) until Commit
// drains them into a new Fossil check-in.
const overlaySchema = `
CREATE TABLE IF NOT EXISTS workspace_overlay (
    path    TEXT PRIMARY KEY,
    content BLOB,
    perm    TEXT NOT NULL DEFAULT '',
    deleted INTEGER NOT NULL DEFAULT 0,
    mtime   INTEGER NOT NULL
);
`

type overlayEntry struct {
	content []byte
	perm    string
	deleted bool
	mtime   int64 // unix seconds
}

// ensureOverlay creates the workspace_overlay table if missing and loads
// existing rows (from a prior process lifetime) into memory.
func (v *VFS) ensureOverlay() error {
	db := v.eng.Repo().DB()
	if _, err := db.Exec(overlaySchema); err != nil {
		return fmt.Errorf("create overlay table: %w", err)
	}
	rows, err := db.Query(
		"SELECT path, content, perm, deleted, mtime FROM workspace_overlay",
	)
	if err != nil {
		return fmt.Errorf("load overlay: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var p, perm string
		var content []byte
		var deletedInt, mtime int64
		if err := rows.Scan(&p, &content, &perm, &deletedInt, &mtime); err != nil {
			return fmt.Errorf("scan overlay row: %w", err)
		}
		v.overlay[p] = &overlayEntry{
			content: content,
			perm:    perm,
			deleted: deletedInt != 0,
			mtime:   mtime,
		}
	}
	return rows.Err()
}

// overlayPutContent records a write of content to filePath in the overlay,
// both in the in-memory map and the SQLite row.
func (v *VFS) overlayPutContent(filePath string, content []byte, perm string, mtime int64) error {
	if !v.enableWrites {
		return errReadOnly
	}
	db := v.eng.Repo().DB()
	_, err := db.Exec(
		`INSERT OR REPLACE INTO workspace_overlay(path, content, perm, deleted, mtime)
		 VALUES (?, ?, ?, 0, ?)`,
		filePath, content, perm, mtime,
	)
	if err != nil {
		return fmt.Errorf("overlay put: %w", err)
	}
	v.overlayMu.Lock()
	v.overlay[filePath] = &overlayEntry{
		content: append([]byte(nil), content...),
		perm:    perm,
		deleted: false,
		mtime:   mtime,
	}
	v.overlayMu.Unlock()

	v.sizeMu.Lock()
	v.sizes[filePath] = int64(len(content))
	v.sizeMu.Unlock()
	return nil
}

// overlayMarkDeleted writes a tombstone for filePath, masking any
// previously committed version of the file from reads.
func (v *VFS) overlayMarkDeleted(filePath string, mtime int64) error {
	if !v.enableWrites {
		return errReadOnly
	}
	db := v.eng.Repo().DB()
	_, err := db.Exec(
		`INSERT OR REPLACE INTO workspace_overlay(path, content, perm, deleted, mtime)
		 VALUES (?, NULL, '', 1, ?)`,
		filePath, mtime,
	)
	if err != nil {
		return fmt.Errorf("overlay delete: %w", err)
	}
	v.overlayMu.Lock()
	v.overlay[filePath] = &overlayEntry{deleted: true, mtime: mtime}
	v.overlayMu.Unlock()

	v.sizeMu.Lock()
	delete(v.sizes, filePath)
	v.sizeMu.Unlock()
	return nil
}

// overlayClearAll drops every row in the overlay table inside a single
// transaction. Called by Commit after a successful drain.
func (v *VFS) overlayClearAll() error {
	db := v.eng.Repo().DB()
	if _, err := db.Exec(`DELETE FROM workspace_overlay`); err != nil {
		return fmt.Errorf("overlay clear: %w", err)
	}
	v.overlayMu.Lock()
	v.overlay = make(map[string]*overlayEntry)
	v.overlayMu.Unlock()
	return nil
}

// overlayLookup returns the overlay entry for the given path, if any.
func (v *VFS) overlayLookup(filePath string) (*overlayEntry, bool) {
	v.overlayMu.RLock()
	defer v.overlayMu.RUnlock()
	e, ok := v.overlay[filePath]
	return e, ok
}

// overlayList returns a snapshot of every overlay row.
func (v *VFS) overlayList() []overlayPathEntry {
	v.overlayMu.RLock()
	defer v.overlayMu.RUnlock()
	out := make([]overlayPathEntry, 0, len(v.overlay))
	for p, e := range v.overlay {
		out = append(out, overlayPathEntry{path: p, entry: *e})
	}
	return out
}

type overlayPathEntry struct {
	path  string
	entry overlayEntry
}

var (
	errReadOnly = errors.New("vfs: writes not enabled in this VFS instance")
	// ErrMergeRequired is returned by Commit when the local agent's repo
	// has diverged from the autosync peer; the agent must merge before
	// committing again. Stub for the future Sync wiring; not yet returned.
	ErrMergeRequired = errors.New("vfs: local diverged from peer; merge required")
	// ErrNothingToCommit is returned by Commit when the overlay has no
	// rows that would land in a new check-in (empty overlay, or the
	// overlay only contains macOS metadata that's being stripped).
	ErrNothingToCommit = errors.New("vfs: nothing to commit")
)

// sentinel to placate linter when unused in current step
var _ = sql.ErrNoRows
