// Package vfs implements an io/fs.FS view of a Fossil repository at a
// specific checkin, with optional writable overlay semantics.
//
// Reads consult the overlay first (uncommitted writes), then fall through
// to the manifest's libfossil-backed content. Writes go to a SQLite
// workspace_overlay table inside the agent's own Fossil repo file; Commit
// drains the overlay into a new check-in.
//
// See docs/design.md for the full design — particularly §5 (isolation
// & overlay) and §6 (Go API).
package vfs

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/danmestas/fslite/engine"
)

// Config opens a VFS instance at a specific checkin of a Fossil repository.
type Config struct {
	// RepoPath is the on-disk path to the Fossil SQLite repo.
	RepoPath string
	// Version is a symbolic checkin name ("tip", "trunk", branch, UUID, or
	// UUID prefix). Empty defaults to "tip".
	Version string
	// EnableWrites turns on the workspace_overlay table and the write-path
	// methods (OpenFile, Remove, Mkdir, Rename, Commit). Default false =
	// read-only.
	EnableWrites bool
	// User is the Fossil user attribution recorded on commits. Default
	// "fslite".
	User string
	// Sync configures NATS-mediated autosync with peer VFS instances
	// sharing the same ProjectCode. Leave the zero value to disable.
	Sync SyncConfig
	// NoNATS forces local-only mode: skip all NATS wiring (autosync,
	// commit notifications, JetStream-KV-backed cross-agent WebDAV
	// locks) even when SyncConfig is populated. WebDAVHandler falls
	// back to in-process MemLS. Use this when running fslite as a
	// pure userspace VFS where multi-agent and cross-host coordination
	// aren't needed.
	NoNATS bool
	// IgnoreGlob overrides the per-repo `vfs-ignore-glob` config key
	// and the built-in DefaultIgnoreGlob. Comma-separated patterns;
	// basename match when the pattern has no "/", full-path match
	// otherwise. The sentinel value "-" disables filtering — every
	// overlay row is committed, including sidecars. See vfs/ignore.go.
	IgnoreGlob string
	// AutoCommit, when non-zero, enables a debounced auto-commit: the
	// overlay is drained into a new check-in after AutoCommit elapses
	// with no further writes. Each write resets the timer. Default 0
	// = disabled (manual `Commit` only). Suggested setting: 10s.
	// Auto-commits use a generated message ("auto: 3 files in src/").
	AutoCommit time.Duration
}

// VFS implements fs.FS, fs.ReadDirFS, and fs.StatFS over a Fossil checkin,
// plus a writable surface (OpenFile, Remove, Rename, Mkdir, Commit) when
// EnableWrites is true.
type VFS struct {
	eng     *engine.Engine
	rid     int64
	user    string
	modTime time.Time

	enableWrites bool
	ignore       *ignoreMatcher

	// manifestFiles is the immutable snapshot of the checkin's tree at
	// construction. The view tree is rebuilt from this + overlay on writes.
	manifestFiles []engine.FileEntry

	viewMu sync.RWMutex
	view   *node

	overlayMu sync.RWMutex
	overlay   map[string]*overlayEntry

	sizeMu sync.Mutex
	sizes  map[string]int64

	// sync is nil when SyncConfig is not configured. When non-nil,
	// Commit publishes notifications and incoming notifications trigger
	// pulls from peers.
	sync *SyncRunner

	// lockSystem is the JetStream-KV-backed cross-agent WebDAV
	// LockSystem; nil when sync is disabled (WebDAVHandler falls back
	// to MemLS in that case).
	lockSystem *natsLockSystem

	// Auto-commit state (Config.AutoCommit > 0). Each overlay write
	// resets the timer; when it fires, the overlay drains into a
	// check-in. Close stops the timer.
	autoCommit       time.Duration
	autoCommitMu     sync.Mutex
	autoCommitTimer  *time.Timer
	autoCommitClosed bool
}

// CheckinID identifies a Fossil check-in by RID + UUID.
type CheckinID struct {
	RID  int64
	UUID string
}

// New opens a VFS rooted at the configured checkin.
func New(cfg Config) (*VFS, error) {
	eng, err := engine.Open(cfg.RepoPath)
	if err != nil {
		return nil, err
	}

	version := cfg.Version
	if version == "" {
		version = "tip"
	}
	rid, err := eng.ResolveVersion(version)
	if err != nil {
		eng.Close()
		return nil, err
	}

	files, err := eng.ListFilesAt(rid)
	if err != nil {
		eng.Close()
		return nil, err
	}

	user := cfg.User
	if user == "" {
		user = "fslite"
	}

	v := &VFS{
		eng:           eng,
		rid:           rid,
		user:          user,
		modTime:       time.Now(),
		enableWrites:  cfg.EnableWrites,
		ignore:        resolveIgnore(eng, cfg.IgnoreGlob),
		manifestFiles: files,
		overlay:       map[string]*overlayEntry{},
		sizes:         map[string]int64{},
		autoCommit:    cfg.AutoCommit,
	}

	if cfg.EnableWrites {
		if err := v.ensureOverlay(); err != nil {
			eng.Close()
			return nil, err
		}
	}
	v.rebuildView()

	if cfg.Sync.NATS != nil && !cfg.NoNATS {
		if err := v.attachSync(cfg.Sync); err != nil {
			eng.Close()
			return nil, err
		}
	}
	return v, nil
}

// Sync returns the VFS's SyncRunner, or nil if SyncConfig was not set.
// Used to drive explicit operations like PullFromPeer.
func (v *VFS) Sync() *SyncRunner {
	return v.sync
}

// IgnoreGlob returns the currently effective ignore-glob string and
// where it came from ("override" = Config.IgnoreGlob, "config" = the
// repo's vfs-ignore-glob setting, "default" = built-in, "disabled" =
// filtering off).
func (v *VFS) IgnoreGlob() (source, glob string) {
	return v.ignore.effective()
}

// SetIgnoreGlob persists glob to the repo's vfs-ignore-glob config key
// AND swaps the in-memory matcher so subsequent commits use it
// immediately. Empty glob clears the setting (defaults take over on
// next read). Pass "-" to disable filtering entirely.
func (v *VFS) SetIgnoreGlob(glob string) error {
	if err := v.eng.Repo().SetConfig(FossilIgnoreKey, glob); err != nil {
		return err
	}
	if glob == "" {
		v.ignore = newIgnoreMatcher(DefaultIgnoreGlob, "default")
	} else {
		v.ignore = newIgnoreMatcher(glob, "config")
	}
	return nil
}

// Close releases the underlying Fossil repository handle and tears down
// any NATS sync subscriptions + the auto-commit timer.
func (v *VFS) Close() error {
	v.autoCommitMu.Lock()
	v.autoCommitClosed = true
	if v.autoCommitTimer != nil {
		v.autoCommitTimer.Stop()
	}
	v.autoCommitMu.Unlock()
	if v.sync != nil {
		v.sync.close()
	}
	return v.eng.Close()
}

// bumpAutoCommit (re)arms the auto-commit debounce timer. No-op when
// AutoCommit is disabled or the VFS is closing.
func (v *VFS) bumpAutoCommit() {
	if v.autoCommit <= 0 {
		return
	}
	v.autoCommitMu.Lock()
	defer v.autoCommitMu.Unlock()
	if v.autoCommitClosed {
		return
	}
	if v.autoCommitTimer == nil {
		v.autoCommitTimer = time.AfterFunc(v.autoCommit, v.autoCommitFire)
	} else {
		v.autoCommitTimer.Reset(v.autoCommit)
	}
}

// autoCommitFire is the timer callback: drains the overlay into a
// check-in with a generated message. ErrNothingToCommit is silently
// swallowed (the timer fired but everything had already been
// committed by an explicit `Commit` call, or the overlay was all
// ignore-glob matches).
func (v *VFS) autoCommitFire() {
	v.autoCommitMu.Lock()
	if v.autoCommitClosed {
		v.autoCommitMu.Unlock()
		return
	}
	v.autoCommitMu.Unlock()
	msg := v.autoCommitMessage()
	if _, err := v.Commit(msg); err != nil && !errors.Is(err, ErrNothingToCommit) {
		// Swallow — auto-commit is best-effort. The next explicit
		// `Commit` will surface the same error if it persists.
		_ = err
	}
}

// autoCommitMessage builds a one-line summary of the overlay's current
// pending changes, grouped by top-level directory. Examples:
//   - "auto: 3 files in src/"
//   - "auto: 2 files in src/, 1 file in docs/"
//   - "auto: 1 file" (file at root)
func (v *VFS) autoCommitMessage() string {
	rows := v.overlayList()
	if len(rows) == 0 {
		return "auto: no pending changes"
	}
	counts := map[string]int{}
	for _, r := range rows {
		top := "/"
		if i := strings.Index(r.path, "/"); i >= 0 {
			top = r.path[:i+1]
		}
		counts[top]++
	}
	dirs := make([]string, 0, len(counts))
	for d := range counts {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	parts := make([]string, 0, len(dirs))
	for _, d := range dirs {
		label := fmt.Sprintf("%d files", counts[d])
		if counts[d] == 1 {
			label = "1 file"
		}
		if d != "/" {
			label += " in " + d
		}
		parts = append(parts, label)
		if len(parts) == 3 {
			break
		}
	}
	return "auto: " + strings.Join(parts, ", ")
}

// ---------------- fs.FS / fs.ReadDirFS / fs.StatFS ----------------

func (v *VFS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	if e, ok := v.overlayLookup(name); ok && e.deleted {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	if e, ok := v.overlayLookup(name); ok {
		return v.openOverlayFile(name, e), nil
	}
	n := v.lookup(name)
	if n == nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	if n.isDir {
		return v.openDir(name, n), nil
	}
	return v.openManifestFile(name, n)
}

func (v *VFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrInvalid}
	}
	n := v.lookup(name)
	if n == nil {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrNotExist}
	}
	if !n.isDir {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: errors.New("not a directory")}
	}
	return v.dirEntries(name, n), nil
}

func (v *VFS) Stat(name string) (fs.FileInfo, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrInvalid}
	}
	if e, ok := v.overlayLookup(name); ok && e.deleted {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
	}
	if e, ok := v.overlayLookup(name); ok {
		return v.fileInfoForOverlay(name, e), nil
	}
	n := v.lookup(name)
	if n == nil {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
	}
	if n.isDir {
		return v.dirInfo(name, n), nil
	}
	size, err := v.fileSize(name)
	if err != nil {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: err}
	}
	return v.manifestFileInfo(name, n, size), nil
}

// ---------------- Writable surface ----------------

// OpenFile returns a writable handle when flag carries O_WRONLY, O_RDWR,
// O_APPEND, O_CREATE, or O_TRUNC. The standard io/fs file flags from
// package os are accepted (os.O_CREATE etc).
//
// For pure-read flags (flag == os.O_RDONLY), OpenFile is equivalent to Open.
func (v *VFS) OpenFile(name string, flag int, perm fs.FileMode) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "openfile", Path: name, Err: fs.ErrInvalid}
	}
	mode := flag & 3
	wantWrite := mode == os.O_WRONLY || mode == os.O_RDWR

	if !wantWrite {
		return v.Open(name)
	}
	if !v.enableWrites {
		return nil, &fs.PathError{Op: "openfile", Path: name, Err: errReadOnly}
	}

	existing, hasExisting := v.readExisting(name)
	created := !hasExisting
	if created && (flag&os.O_CREATE) == 0 {
		return nil, &fs.PathError{Op: "openfile", Path: name, Err: fs.ErrNotExist}
	}

	var buf []byte
	if !created && (flag&os.O_TRUNC) == 0 {
		buf = append(buf, existing...)
	}

	permStr := ""
	if perm&0111 != 0 {
		permStr = "x"
	}

	return &vfsWriteFile{
		v:      v,
		name:   name,
		perm:   permStr,
		buf:    buf,
		offset: int64(len(buf)),
		append: flag&os.O_APPEND != 0,
	}, nil
}

// Remove writes a tombstone for name. Subsequent reads return ErrNotExist
// until the next Commit or until a new write at the same path.
func (v *VFS) Remove(name string) error {
	if !v.enableWrites {
		return &fs.PathError{Op: "remove", Path: name, Err: errReadOnly}
	}
	if !fs.ValidPath(name) {
		return &fs.PathError{Op: "remove", Path: name, Err: fs.ErrInvalid}
	}
	// Verify the path exists either in overlay (non-deleted) or in tree.
	if e, ok := v.overlayLookup(name); ok && e.deleted {
		return &fs.PathError{Op: "remove", Path: name, Err: fs.ErrNotExist}
	}
	if _, ok := v.overlayLookup(name); !ok {
		n := v.lookup(name)
		if n == nil {
			return &fs.PathError{Op: "remove", Path: name, Err: fs.ErrNotExist}
		}
		if n.isDir {
			// removal of dirs not in V1 — Fossil tracks no empty dirs anyway
			return &fs.PathError{Op: "remove", Path: name, Err: errIsDir}
		}
	}
	if err := v.overlayMarkDeleted(name, time.Now().Unix()); err != nil {
		return &fs.PathError{Op: "remove", Path: name, Err: err}
	}
	v.rebuildView()
	return nil
}

// Rename moves oldPath to newPath atomically against the overlay.
func (v *VFS) Rename(oldPath, newPath string) error {
	if !v.enableWrites {
		return &fs.PathError{Op: "rename", Path: oldPath, Err: errReadOnly}
	}
	if !fs.ValidPath(oldPath) || !fs.ValidPath(newPath) {
		return &fs.PathError{Op: "rename", Path: oldPath, Err: fs.ErrInvalid}
	}
	// Read source content + perm.
	content, perm, err := v.readWithPerm(oldPath)
	if err != nil {
		return &fs.PathError{Op: "rename", Path: oldPath, Err: err}
	}
	now := time.Now().Unix()
	if err := v.overlayPutContent(newPath, content, perm, now); err != nil {
		return &fs.PathError{Op: "rename", Path: newPath, Err: err}
	}
	if err := v.overlayMarkDeleted(oldPath, now); err != nil {
		return &fs.PathError{Op: "rename", Path: oldPath, Err: err}
	}
	v.rebuildView()
	return nil
}

// Mkdir is a no-op for non-existent paths — Fossil does not track empty
// directories. Returns nil for any valid path; the directory materialises
// the moment a file is written under it. Returns fs.ErrExist only if name
// already exists as a regular file.
func (v *VFS) Mkdir(name string, _ fs.FileMode) error {
	if !v.enableWrites {
		return &fs.PathError{Op: "mkdir", Path: name, Err: errReadOnly}
	}
	if !fs.ValidPath(name) {
		return &fs.PathError{Op: "mkdir", Path: name, Err: fs.ErrInvalid}
	}
	if name == "." {
		return nil
	}
	if e, ok := v.overlayLookup(name); ok && !e.deleted {
		return &fs.PathError{Op: "mkdir", Path: name, Err: fs.ErrExist}
	}
	n := v.lookup(name)
	if n != nil && !n.isDir {
		return &fs.PathError{Op: "mkdir", Path: name, Err: fs.ErrExist}
	}
	return nil
}

// Commit drains the overlay into a new Fossil check-in, then truncates
// the overlay table. The VFS re-roots at the new check-in on success.
//
// Overlay rows matching the ignore matcher (see vfs/ignore.go) are
// silently dropped from the drain. The overlay rows are still cleared
// so the in-memory state matches the manifest after commit; the host
// OS will recreate sidecars on next interaction.
//
// Returns ErrNothingToCommit when the overlay holds no rows that would
// be committed (empty, or only ignored entries). Returns ErrMergeRequired
// wiring if the local agent has diverged from the autosync peer (not
// yet active until step 5 wires NATS).
func (v *VFS) Commit(message string) (CheckinID, error) {
	if !v.enableWrites {
		return CheckinID{}, errReadOnly
	}
	overlay := v.overlayList()
	filtered := overlay[:0]
	for _, oe := range overlay {
		if !v.ignore.match(oe.path) {
			filtered = append(filtered, oe)
		}
	}
	overlay = filtered
	if len(overlay) == 0 {
		return CheckinID{}, ErrNothingToCommit
	}

	rid, uuid, err := v.commitDrain(overlay, message)
	if err != nil {
		return CheckinID{}, err
	}
	if err := v.overlayClearAll(); err != nil {
		return CheckinID{RID: rid, UUID: uuid}, err
	}

	files, err := v.eng.ListFilesAt(rid)
	if err != nil {
		return CheckinID{RID: rid, UUID: uuid}, err
	}
	v.viewMu.Lock()
	v.manifestFiles = files
	v.rid = rid
	v.modTime = time.Now()
	v.view = buildTree(files)
	v.viewMu.Unlock()

	v.sizeMu.Lock()
	v.sizes = map[string]int64{}
	v.sizeMu.Unlock()

	ck := CheckinID{RID: rid, UUID: uuid}
	if v.sync != nil {
		_ = v.sync.publishCommit(ck)
	}
	return ck, nil
}

// commitDrain reconstructs the target file set from the parent check-in
// + the overlay deltas and submits it to libfossil. Deletions force the
// PartialManifest path; pure additions/modifications use the cheaper
// default-merge path.
func (v *VFS) commitDrain(overlay []overlayPathEntry, message string) (int64, string, error) {
	hasDeletes := false
	for _, oe := range overlay {
		if oe.entry.deleted {
			hasDeletes = true
			break
		}
	}

	overlayMap := make(map[string]overlayEntry, len(overlay))
	for _, oe := range overlay {
		overlayMap[oe.path] = oe.entry
	}

	var files []engine.CommitFile
	if hasDeletes {
		// Reconstruct full tree: parent files (with overlay overrides),
		// minus tombstones, plus overlay-only additions.
		parentFiles, err := v.eng.ListFilesAt(v.rid)
		if err != nil {
			return 0, "", err
		}
		seen := map[string]bool{}
		for _, pf := range parentFiles {
			if e, ok := overlayMap[pf.Name]; ok {
				if e.deleted {
					seen[pf.Name] = true
					continue
				}
				files = append(files, engine.CommitFile{
					Name:    pf.Name,
					Content: e.content,
					Perm:    e.perm,
				})
				seen[pf.Name] = true
				continue
			}
			// Carry forward unchanged: re-read content (libfossil dedups
			// by hash on store, so no duplicate storage).
			content, err := v.eng.ReadFile(v.rid, pf.Name)
			if err != nil {
				return 0, "", err
			}
			files = append(files, engine.CommitFile{Name: pf.Name, Content: content, Perm: pf.Perm})
			seen[pf.Name] = true
		}
		for p, e := range overlayMap {
			if e.deleted || seen[p] {
				continue
			}
			files = append(files, engine.CommitFile{Name: p, Content: e.content, Perm: e.perm})
		}
	} else {
		for p, e := range overlayMap {
			files = append(files, engine.CommitFile{Name: p, Content: e.content, Perm: e.perm})
		}
	}

	return v.eng.CommitFiles(files, message, v.user, v.rid, hasDeletes)
}

// ---------------- helpers ----------------

func (v *VFS) lookup(name string) *node {
	v.viewMu.RLock()
	defer v.viewMu.RUnlock()
	if name == "." {
		return v.view
	}
	parts := strings.Split(name, "/")
	cur := v.view
	for _, p := range parts {
		if !cur.isDir {
			return nil
		}
		next, ok := cur.children[p]
		if !ok {
			return nil
		}
		cur = next
	}
	return cur
}

// rebuildView reconstructs the merged manifest+overlay tree.
func (v *VFS) rebuildView() {
	v.overlayMu.RLock()
	overlayCopy := make(map[string]*overlayEntry, len(v.overlay))
	for k, e := range v.overlay {
		overlayCopy[k] = e
	}
	v.overlayMu.RUnlock()

	files := make([]engine.FileEntry, 0, len(v.manifestFiles)+len(overlayCopy))
	seen := make(map[string]bool, len(v.manifestFiles))

	for _, mf := range v.manifestFiles {
		if e, ok := overlayCopy[mf.Name]; ok {
			if e.deleted {
				seen[mf.Name] = true
				continue
			}
			files = append(files, engine.FileEntry{
				Name: mf.Name, UUID: mf.UUID, Perm: e.perm,
			})
			seen[mf.Name] = true
			continue
		}
		files = append(files, mf)
		seen[mf.Name] = true
	}
	for p, e := range overlayCopy {
		if e.deleted || seen[p] {
			continue
		}
		files = append(files, engine.FileEntry{Name: p, Perm: e.perm})
	}

	v.viewMu.Lock()
	v.view = buildTree(files)
	v.viewMu.Unlock()
}

func (v *VFS) readExisting(filePath string) ([]byte, bool) {
	if e, ok := v.overlayLookup(filePath); ok {
		if e.deleted {
			return nil, false
		}
		return append([]byte(nil), e.content...), true
	}
	n := v.lookup(filePath)
	if n == nil || n.isDir {
		return nil, false
	}
	data, err := v.eng.ReadFile(v.rid, filePath)
	if err != nil {
		return nil, false
	}
	return data, true
}

func (v *VFS) readWithPerm(filePath string) ([]byte, string, error) {
	if e, ok := v.overlayLookup(filePath); ok {
		if e.deleted {
			return nil, "", fs.ErrNotExist
		}
		return append([]byte(nil), e.content...), e.perm, nil
	}
	n := v.lookup(filePath)
	if n == nil {
		return nil, "", fs.ErrNotExist
	}
	if n.isDir {
		return nil, "", errIsDir
	}
	data, err := v.eng.ReadFile(v.rid, filePath)
	if err != nil {
		return nil, "", err
	}
	return data, n.perm, nil
}

func (v *VFS) fileSize(filePath string) (int64, error) {
	v.sizeMu.Lock()
	if sz, ok := v.sizes[filePath]; ok {
		v.sizeMu.Unlock()
		return sz, nil
	}
	v.sizeMu.Unlock()

	data, err := v.eng.ReadFile(v.rid, filePath)
	if err != nil {
		return 0, err
	}
	sz := int64(len(data))
	v.sizeMu.Lock()
	v.sizes[filePath] = sz
	v.sizeMu.Unlock()
	return sz, nil
}

func (v *VFS) openManifestFile(filePath string, n *node) (fs.File, error) {
	data, err := v.eng.ReadFile(v.rid, filePath)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: filePath, Err: err}
	}
	v.sizeMu.Lock()
	v.sizes[filePath] = int64(len(data))
	v.sizeMu.Unlock()
	return &vfsFile{
		info:    v.manifestFileInfo(filePath, n, int64(len(data))),
		content: data,
	}, nil
}

func (v *VFS) openOverlayFile(filePath string, e *overlayEntry) fs.File {
	return &vfsFile{
		info:    v.fileInfoForOverlay(filePath, e),
		content: append([]byte(nil), e.content...),
	}
}

func (v *VFS) openDir(dirPath string, n *node) fs.File {
	return &vfsDir{
		info:    v.dirInfo(dirPath, n),
		entries: v.dirEntries(dirPath, n),
	}
}

func (v *VFS) dirEntries(dirPath string, n *node) []fs.DirEntry {
	names := make([]string, 0, len(n.children))
	for name := range n.children {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]fs.DirEntry, 0, len(names))
	for _, name := range names {
		child := n.children[name]
		childPath := name
		if dirPath != "." {
			childPath = path.Join(dirPath, name)
		}
		out = append(out, &dirEntry{
			name:  name,
			isDir: child.isDir,
			vfs:   v,
			path:  childPath,
			node:  child,
		})
	}
	return out
}

func (v *VFS) manifestFileInfo(filePath string, n *node, size int64) fs.FileInfo {
	mode := fs.FileMode(0644)
	if n.perm == "x" {
		mode = 0755
	}
	return &fileInfo{
		name:    path.Base(filePath),
		size:    size,
		mode:    mode,
		modTime: v.modTime,
	}
}

func (v *VFS) fileInfoForOverlay(filePath string, e *overlayEntry) fs.FileInfo {
	mode := fs.FileMode(0644)
	if e.perm == "x" {
		mode = 0755
	}
	return &fileInfo{
		name:    path.Base(filePath),
		size:    int64(len(e.content)),
		mode:    mode,
		modTime: time.Unix(e.mtime, 0),
	}
}

func (v *VFS) dirInfo(dirPath string, _ *node) fs.FileInfo {
	name := "."
	if dirPath != "." {
		name = path.Base(dirPath)
	}
	return &fileInfo{
		name:    name,
		mode:    fs.ModeDir | 0755,
		modTime: v.modTime,
		isDir:   true,
	}
}

// ---------------- tree ----------------

type node struct {
	isDir    bool
	perm     string
	children map[string]*node
}

func buildTree(files []engine.FileEntry) *node {
	root := &node{isDir: true, children: map[string]*node{}}
	for _, f := range files {
		parts := strings.Split(f.Name, "/")
		cur := root
		for i, p := range parts {
			isLeaf := i == len(parts)-1
			if existing, ok := cur.children[p]; ok {
				cur = existing
				continue
			}
			child := &node{}
			if isLeaf {
				child.isDir = false
				child.perm = f.Perm
			} else {
				child.isDir = true
				child.children = map[string]*node{}
			}
			cur.children[p] = child
			cur = child
		}
	}
	return root
}

// ---------------- file/dir handle types ----------------

// vfsFile is a read-only handle backed by content held in memory.
type vfsFile struct {
	info    fs.FileInfo
	content []byte
	offset  int64
	closed  bool
}

func (f *vfsFile) Stat() (fs.FileInfo, error) {
	if f.closed {
		return nil, fs.ErrClosed
	}
	return f.info, nil
}

func (f *vfsFile) Read(p []byte) (int, error) {
	if f.closed {
		return 0, fs.ErrClosed
	}
	if f.offset >= int64(len(f.content)) {
		return 0, io.EOF
	}
	n := copy(p, f.content[f.offset:])
	f.offset += int64(n)
	return n, nil
}

func (f *vfsFile) Seek(offset int64, whence int) (int64, error) {
	if f.closed {
		return 0, fs.ErrClosed
	}
	switch whence {
	case io.SeekStart:
		f.offset = offset
	case io.SeekCurrent:
		f.offset += offset
	case io.SeekEnd:
		f.offset = int64(len(f.content)) + offset
	default:
		return 0, errors.New("vfs: invalid seek whence")
	}
	if f.offset < 0 {
		f.offset = 0
	}
	return f.offset, nil
}

func (f *vfsFile) Close() error {
	f.closed = true
	f.content = nil
	return nil
}

// vfsWriteFile is a writable handle. Writes accumulate in buf; the buf
// is flushed to the overlay on Close.
type vfsWriteFile struct {
	v       *VFS
	name    string
	perm    string
	buf     []byte
	offset  int64
	append  bool
	dirty   bool
	closed  bool
}

func (f *vfsWriteFile) Stat() (fs.FileInfo, error) {
	if f.closed {
		return nil, fs.ErrClosed
	}
	return &fileInfo{
		name:    path.Base(f.name),
		size:    int64(len(f.buf)),
		mode:    permToMode(f.perm),
		modTime: time.Now(),
	}, nil
}

func (f *vfsWriteFile) Read(p []byte) (int, error) {
	if f.closed {
		return 0, fs.ErrClosed
	}
	if f.offset >= int64(len(f.buf)) {
		return 0, io.EOF
	}
	n := copy(p, f.buf[f.offset:])
	f.offset += int64(n)
	return n, nil
}

func (f *vfsWriteFile) Write(p []byte) (int, error) {
	if f.closed {
		return 0, fs.ErrClosed
	}
	if f.append {
		f.offset = int64(len(f.buf))
	}
	end := f.offset + int64(len(p))
	if end > int64(len(f.buf)) {
		grown := make([]byte, end)
		copy(grown, f.buf)
		f.buf = grown
	}
	n := copy(f.buf[f.offset:], p)
	f.offset += int64(n)
	f.dirty = true
	return n, nil
}

func (f *vfsWriteFile) Seek(offset int64, whence int) (int64, error) {
	if f.closed {
		return 0, fs.ErrClosed
	}
	switch whence {
	case io.SeekStart:
		f.offset = offset
	case io.SeekCurrent:
		f.offset += offset
	case io.SeekEnd:
		f.offset = int64(len(f.buf)) + offset
	default:
		return 0, errors.New("vfs: invalid seek whence")
	}
	if f.offset < 0 {
		f.offset = 0
	}
	return f.offset, nil
}

func (f *vfsWriteFile) Close() error {
	if f.closed {
		return nil
	}
	f.closed = true
	if f.dirty {
		if err := f.v.overlayPutContent(f.name, f.buf, f.perm, time.Now().Unix()); err != nil {
			return err
		}
		f.v.rebuildView()
	}
	f.buf = nil
	return nil
}

// vfsDir implements fs.ReadDirFile.
type vfsDir struct {
	info    fs.FileInfo
	entries []fs.DirEntry
	pos     int
	closed  bool
}

func (d *vfsDir) Stat() (fs.FileInfo, error) {
	if d.closed {
		return nil, fs.ErrClosed
	}
	return d.info, nil
}

func (d *vfsDir) Read([]byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: d.info.Name(), Err: errIsDir}
}

func (d *vfsDir) ReadDir(n int) ([]fs.DirEntry, error) {
	if d.closed {
		return nil, fs.ErrClosed
	}
	remaining := len(d.entries) - d.pos
	if n <= 0 {
		out := d.entries[d.pos:]
		d.pos = len(d.entries)
		return out, nil
	}
	if remaining == 0 {
		return nil, io.EOF
	}
	take := n
	if take > remaining {
		take = remaining
	}
	out := d.entries[d.pos : d.pos+take]
	d.pos += take
	return out, nil
}

func (d *vfsDir) Close() error {
	d.closed = true
	d.entries = nil
	return nil
}

// dirEntry implements fs.DirEntry; Info is lazy (size requires a content read).
type dirEntry struct {
	name  string
	isDir bool
	vfs   *VFS
	path  string
	node  *node
}

func (e *dirEntry) Name() string { return e.name }
func (e *dirEntry) IsDir() bool  { return e.isDir }
func (e *dirEntry) Type() fs.FileMode {
	if e.isDir {
		return fs.ModeDir
	}
	return 0
}
func (e *dirEntry) Info() (fs.FileInfo, error) {
	if e.isDir {
		return e.vfs.dirInfo(e.path, e.node), nil
	}
	if oe, ok := e.vfs.overlayLookup(e.path); ok && !oe.deleted {
		return e.vfs.fileInfoForOverlay(e.path, oe), nil
	}
	size, err := e.vfs.fileSize(e.path)
	if err != nil {
		return nil, err
	}
	return e.vfs.manifestFileInfo(e.path, e.node, size), nil
}

type fileInfo struct {
	name    string
	size    int64
	mode    fs.FileMode
	modTime time.Time
	isDir   bool
}

func (i *fileInfo) Name() string       { return i.name }
func (i *fileInfo) Size() int64        { return i.size }
func (i *fileInfo) Mode() fs.FileMode  { return i.mode }
func (i *fileInfo) ModTime() time.Time { return i.modTime }
func (i *fileInfo) IsDir() bool        { return i.isDir }
func (i *fileInfo) Sys() any           { return nil }

func permToMode(perm string) fs.FileMode {
	if perm == "x" {
		return 0755
	}
	return 0644
}

// errIsDir is returned by Read on a directory handle. Sentinel separate
// from fs.PathError so callers don't need a type assertion.
var errIsDir = errors.New("is a directory")

// Compile-time interface checks.
var (
	_ fs.FS          = (*VFS)(nil)
	_ fs.ReadDirFS   = (*VFS)(nil)
	_ fs.StatFS      = (*VFS)(nil)
	_ fs.ReadDirFile = (*vfsDir)(nil)
	_ io.Writer      = (*vfsWriteFile)(nil)
	_ io.Seeker      = (*vfsWriteFile)(nil)
	_ io.Reader      = (*vfsWriteFile)(nil)
)
