package vfs_test

import (
	"errors"
	"io"
	"io/fs"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// lockTokenRe pulls the token out of a LOCK response's Lock-Token header,
// which is delivered as "<opaquelocktoken:...>" / "<12345>".
var lockTokenRe = regexp.MustCompile(`<([^>]+)>`)

// lockResource LOCKs a path and returns the granted token.
func lockResource(t *testing.T, base, path string) string {
	t.Helper()
	body := []byte(`<?xml version="1.0" encoding="utf-8"?>` +
		`<D:lockinfo xmlns:D="DAV:"><D:lockscope><D:exclusive/></D:lockscope>` +
		`<D:locktype><D:write/></D:locktype>` +
		`<D:owner><D:href>tester</D:href></D:owner></D:lockinfo>`)
	resp := httpDo(t, "LOCK", base+path, body, map[string]string{
		"Timeout":      "Second-3600",
		"Depth":        "0",
		"Content-Type": "application/xml",
	})
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 {
		t.Fatalf("LOCK %s: status %d", path, resp.StatusCode)
	}
	m := lockTokenRe.FindStringSubmatch(resp.Header.Get("Lock-Token"))
	if m == nil {
		t.Fatalf("LOCK %s: no Lock-Token in response (%q)", path, resp.Header.Get("Lock-Token"))
	}
	return m[1]
}

// TestWebDAVMoveWithSourceOnlyLockToken replays the exchange a Linux davfs2
// mount produces during an ordinary atomic save: lock the temp file that was
// just written, then MOVE it onto the (unlocked) target, submitting an If
// header that names only the source.
//
// RFC 4918 only requires a token for resources that are actually locked, so
// this must succeed. x/net/webdav's memLS demands a token for both names and
// answers 412, which surfaces as EIO at the mount and loses the edit — leaving
// the temp file behind to be committed in its place.
func TestWebDAVMoveWithSourceOnlyLockToken(t *testing.T) {
	v, base := newWebDAVServer(t)

	put := httpDo(t, "PUT", base+"/.README.tmp", []byte("edited\n"), nil)
	put.Body.Close()
	if put.StatusCode >= 300 {
		t.Fatalf("PUT temp file: status %d", put.StatusCode)
	}

	token := lockResource(t, base, "/.README.tmp")

	mv := httpDo(t, "MOVE", base+"/.README.tmp", nil, map[string]string{
		"Destination": base + "/README.md",
		"If":          "<" + base + "/.README.tmp> (<" + token + ">)",
	})
	defer mv.Body.Close()
	if mv.StatusCode >= 300 {
		t.Fatalf("MOVE with source-only lock token: status %d, want 2xx", mv.StatusCode)
	}

	if got := readAll(t, v, "README.md"); string(got) != "edited\n" {
		t.Errorf("destination content after MOVE: got %q, want %q", got, "edited\n")
	}
	if _, err := v.Stat(".README.tmp"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("temp file survived the MOVE: %v", err)
	}
}

// TestWebDAVMoveRejectedWhenDestinationLockedByAnother is the other side of the
// relaxation: an unlocked destination is fine, but one held by a different
// client must still refuse the MOVE rather than silently overwriting it.
func TestWebDAVMoveRejectedWhenDestinationLockedByAnother(t *testing.T) {
	_, base := newWebDAVServer(t)

	put := httpDo(t, "PUT", base+"/.README.tmp", []byte("edited\n"), nil)
	put.Body.Close()

	srcToken := lockResource(t, base, "/.README.tmp")
	lockResource(t, base, "/README.md") // held by "another client"; token not submitted

	mv := httpDo(t, "MOVE", base+"/.README.tmp", nil, map[string]string{
		"Destination": base + "/README.md",
		"If":          "<" + base + "/.README.tmp> (<" + srcToken + ">)",
	})
	defer mv.Body.Close()
	if mv.StatusCode < 300 {
		t.Fatalf("MOVE onto a destination locked by another client: status %d, want a failure",
			mv.StatusCode)
	}
	if mv.StatusCode != http.StatusLocked && mv.StatusCode != http.StatusPreconditionFailed {
		t.Errorf("MOVE onto locked destination: status %d, want 423 or 412", mv.StatusCode)
	}
}

// TestWebDAVMoveWithBogusLockTokenStillFails guards against the relaxation
// degrading into "ignore the If header": a token that matches nothing must not
// be treated as an absent one.
func TestWebDAVMoveWithBogusLockTokenStillFails(t *testing.T) {
	_, base := newWebDAVServer(t)

	put := httpDo(t, "PUT", base+"/.README.tmp", []byte("edited\n"), nil)
	put.Body.Close()

	lockResource(t, base, "/.README.tmp") // real lock, but we submit a different token

	mv := httpDo(t, "MOVE", base+"/.README.tmp", nil, map[string]string{
		"Destination": base + "/README.md",
		"If":          "<" + base + "/.README.tmp> (<opaquelocktoken:not-a-real-token>)",
	})
	defer mv.Body.Close()
	if mv.StatusCode < 300 {
		t.Fatalf("MOVE with a bogus lock token: status %d, want a failure", mv.StatusCode)
	}
	if !strings.HasPrefix(mv.Status, "4") {
		t.Errorf("MOVE with a bogus token: status %q, want 4xx", mv.Status)
	}
}
