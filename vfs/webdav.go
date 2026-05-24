package vfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"

	"golang.org/x/net/webdav"
)

// WebDAVFileSystem returns a webdav.FileSystem backed by this VFS.
// Suitable for golang.org/x/net/webdav.Handler.FileSystem. Reads work
// without EnableWrites; PUT/DELETE/MKCOL/MOVE require EnableWrites=true.
func (v *VFS) WebDAVFileSystem() webdav.FileSystem {
	return &webdavFS{v: v}
}

// WebDAVHandler returns an http.Handler that speaks WebDAV against this
// VFS. When SyncConfig was supplied at construction, the LockSystem is
// the JetStream-KV-backed natsLockSystem (cross-agent visible).
// Otherwise it's an in-process advisory MemLS.
//
// The returned handler wraps webdav.Handler in webdavQuirkFixer to work
// around macOS-kernel-WebDAV's atomic-save quirks (see that type's
// docstring). Use WebDAVRawHandler() for the unwrapped form.
func (v *VFS) WebDAVHandler() http.Handler {
	return &webdavQuirkFixer{inner: v.WebDAVRawHandler()}
}

// WebDAVRawHandler returns the underlying golang.org/x/net/webdav.Handler
// without the macOS-quirk middleware. Use this when you want spec-strict
// behaviour against a well-behaved client (or when testing against the
// raw library).
func (v *VFS) WebDAVRawHandler() *webdav.Handler {
	var ls webdav.LockSystem
	if v.lockSystem != nil {
		ls = v.lockSystem
	} else {
		ls = webdav.NewMemLS()
	}
	return &webdav.Handler{
		FileSystem: v.WebDAVFileSystem(),
		LockSystem: ls,
		Logger:     nil,
	}
}

// WebDAVHandlerWithLogger is WebDAVHandler with a Logger attached to the
// underlying webdav.Handler — called for every non-2xx response. Use
// this in --verbose / debug modes so handler-level errors (e.g. 500s
// from FileSystem methods) get a one-liner in the log instead of being
// silently swallowed by the upstream package.
func (v *VFS) WebDAVHandlerWithLogger(logger func(*http.Request, error)) http.Handler {
	raw := v.WebDAVRawHandler()
	raw.Logger = logger
	return &webdavQuirkFixer{inner: raw}
}

// webdavQuirkFixer normalises requests before they reach the upstream
// webdav.Handler. Currently it patches one specific RFC 4918 deviation:
//
//	§9.9.4 — "If no Overwrite header is included in a MOVE request,
//	          the server MUST act as if an Overwrite: T was included."
//
// golang.org/x/net/webdav reads `r.Header.Get("Overwrite") == "T"` for
// MOVE, treating an absent header as FALSE — the opposite of the spec.
// macOS's kernel WebDAV client routinely omits the header during atomic
// saves (Cocoa NSDocument writes a temp file then renames it onto the
// target), so every TextEdit/Pages/Word save fails with 412 Precondition
// Failed → EINVAL → "Invalid argument" at the userspace mv layer.
//
// The fix is to set Overwrite: T on inbound MOVE requests that have no
// header. Explicit "F" is preserved so callers can still request the
// spec-correct no-overwrite behaviour.
//
// Same one-line bug doesn't affect COPY in the upstream package, which
// uses `Overwrite != "F"` (defaults to true) — only MOVE is broken.
type webdavQuirkFixer struct {
	inner http.Handler
}

func (q *webdavQuirkFixer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == "MOVE" && r.Header.Get("Overwrite") == "" {
		r.Header.Set("Overwrite", "T")
	}
	q.inner.ServeHTTP(w, r)
}

// ServeWebDAV starts a blocking HTTP server speaking WebDAV against
// this VFS. addr is passed verbatim to http.ListenAndServe — bind to
// "127.0.0.1:PORT" by default; cross-host binding is the caller's call.
func (v *VFS) ServeWebDAV(addr string) error {
	return http.ListenAndServe(addr, v.WebDAVHandler())
}

// webdavFS adapts our VFS to the webdav.FileSystem interface (which
// requires context.Context everywhere and uses os.FileInfo). Paths
// arrive with a leading slash; we normalise to io/fs convention.
type webdavFS struct{ v *VFS }

func (w *webdavFS) Mkdir(_ context.Context, name string, perm os.FileMode) error {
	return w.v.Mkdir(toFSPath(name), perm)
}

func (w *webdavFS) OpenFile(_ context.Context, name string, flag int, perm os.FileMode) (webdav.File, error) {
	p := toFSPath(name)
	f, err := w.v.OpenFile(p, flag, perm)
	if err != nil {
		return nil, err
	}
	return &webdavFile{file: f, v: w.v, name: p}, nil
}

func (w *webdavFS) RemoveAll(_ context.Context, name string) error {
	return w.v.Remove(toFSPath(name))
}

func (w *webdavFS) Rename(_ context.Context, oldName, newName string) error {
	return w.v.Rename(toFSPath(oldName), toFSPath(newName))
}

func (w *webdavFS) Stat(_ context.Context, name string) (os.FileInfo, error) {
	return w.v.Stat(toFSPath(name))
}

// webdavFile wraps any of our fs.File implementations (vfsFile,
// vfsWriteFile, vfsDir) and bolts on the methods webdav.File needs that
// io/fs doesn't supply: Write, Seek (already on our files), and
// Readdir(count) returning []os.FileInfo.
type webdavFile struct {
	file fs.File
	v    *VFS
	name string
}

func (w *webdavFile) Read(p []byte) (int, error)    { return w.file.Read(p) }
func (w *webdavFile) Close() error                  { return w.file.Close() }
func (w *webdavFile) Stat() (os.FileInfo, error)    { return w.file.Stat() }

func (w *webdavFile) Seek(offset int64, whence int) (int64, error) {
	if s, ok := w.file.(io.Seeker); ok {
		return s.Seek(offset, whence)
	}
	return 0, fmt.Errorf("vfs: file %q is not seekable", w.name)
}

func (w *webdavFile) Write(p []byte) (int, error) {
	wr, ok := w.file.(io.Writer)
	if !ok {
		return 0, &fs.PathError{Op: "write", Path: w.name, Err: errReadOnly}
	}
	return wr.Write(p)
}

func (w *webdavFile) Readdir(count int) ([]os.FileInfo, error) {
	rd, ok := w.file.(fs.ReadDirFile)
	if !ok {
		return nil, &fs.PathError{Op: "readdir", Path: w.name, Err: errors.New("not a directory")}
	}
	entries, err := rd.ReadDir(count)
	if err != nil {
		return nil, err
	}
	infos := make([]os.FileInfo, len(entries))
	for i, e := range entries {
		fi, err := e.Info()
		if err != nil {
			return nil, err
		}
		infos[i] = fi
	}
	return infos, nil
}

// toFSPath normalises a WebDAV path (always starts with "/") to the
// io/fs convention (no leading slash; root is "."). Multiple slashes
// are collapsed via path.Clean.
func toFSPath(p string) string {
	p = path.Clean("/" + p)
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		return "."
	}
	return p
}
