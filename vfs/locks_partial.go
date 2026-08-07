package vfs

import (
	"time"

	"golang.org/x/net/webdav"
)

// partialTokenLS relaxes webdav.LockSystem.Confirm to the rule RFC 4918
// actually states: a lock token is only required for resources that are
// themselves locked.
//
// golang.org/x/net/webdav's in-memory LockSystem is stricter than that. On a
// MOVE the handler calls Confirm(src, dst, conditions) and memLS demands that
// *both* names resolve to a lock covered by a submitted token; an unlocked
// destination resolves to nothing, so Confirm fails and the request comes back
// 412 Precondition Failed.
//
// That combination is exactly what a Linux davfs2 mount produces during an
// ordinary atomic save. davfs2 locks the temp file it just created, then
// renames it over the target:
//
//	LOCK /contracts/.msa.tmp          -> 200, token T
//	MOVE /contracts/.msa.tmp
//	     If: <.../contracts/.msa.tmp> (<T>)
//	     Destination: /contracts/msa.md
//
// The destination is unlocked and the client correctly submits no token for
// it, so the MOVE fails, the rename surfaces as EIO, and the editor's save is
// lost — worse, the leftover temp file is what ends up in the next check-in.
//
// natsLockSystem already implements the permissive rule (it only rejects a
// name that is genuinely locked with no matching token). This wrapper gives
// the local-only path the same semantics, so behaviour doesn't depend on
// whether sync happens to be enabled.
//
// Genuine conflicts are still refused: a name with no submitted token is
// confirmed by taking a temporary lock on it, which fails with ErrLocked when
// another client holds it — the same mechanism the handler uses when a request
// carries no If header at all.
type partialTokenLS struct {
	inner webdav.LockSystem
}

var _ webdav.LockSystem = (*partialTokenLS)(nil)

func (l *partialTokenLS) Confirm(now time.Time, name0, name1 string, conditions ...webdav.Condition) (func(), error) {
	release, err := l.inner.Confirm(now, name0, name1, conditions...)
	if err != webdav.ErrConfirmationFailed || name0 == "" || name1 == "" {
		// Either it was fine, it failed for a reason we shouldn't paper over,
		// or there is only one name — in which case strict and permissive
		// agree and there is nothing to relax.
		return release, err
	}

	// Both names were required together and at least one wasn't covered by a
	// submitted token. Confirm them one at a time so an unlocked name doesn't
	// invalidate a token that legitimately covers the other.
	release0, err := l.confirmOne(now, name0, conditions...)
	if err != nil {
		return nil, err
	}
	release1, err := l.confirmOne(now, name1, conditions...)
	if err != nil {
		release0()
		return nil, err
	}
	return func() {
		release1()
		release0()
	}, nil
}

// confirmOne confirms a single name, falling back to a temporary lock when the
// caller submitted no token covering it. The temporary lock is what
// distinguishes "unlocked, so no token was needed" from "locked by someone
// else": Create reports ErrLocked for the latter.
func (l *partialTokenLS) confirmOne(now time.Time, name string, conditions ...webdav.Condition) (func(), error) {
	release, err := l.inner.Confirm(now, name, "", conditions...)
	if err == nil {
		return release, nil
	}
	if err != webdav.ErrConfirmationFailed {
		return nil, err
	}

	token, err := l.inner.Create(now, webdav.LockDetails{
		Root:      name,
		Duration:  -1, // webdav.infiniteTimeout; released below, not by expiry.
		ZeroDepth: true,
	})
	if err == webdav.ErrLocked {
		// Genuinely held by someone else. Report it the way the strict path
		// does, so the handler runs its normal precondition handling and
		// answers 412 (RFC 4918 §10.4.1) rather than a 500.
		return nil, webdav.ErrConfirmationFailed
	}
	if err != nil {
		return nil, err
	}
	return func() { l.inner.Unlock(time.Now(), token) }, nil
}

func (l *partialTokenLS) Create(now time.Time, details webdav.LockDetails) (string, error) {
	return l.inner.Create(now, details)
}

func (l *partialTokenLS) Refresh(now time.Time, token string, duration time.Duration) (webdav.LockDetails, error) {
	return l.inner.Refresh(now, token, duration)
}

func (l *partialTokenLS) Unlock(now time.Time, token string) error {
	return l.inner.Unlock(now, token)
}
