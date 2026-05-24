// Ignore-glob handling for the VFS commit drain.
//
// At commit time the overlay can contain sentinel files written by the
// host OS (macOS sidecars, Windows Thumbs.db, Linux trash directories)
// or editors (vim swap, emacs autosaves). These don't belong in the
// Fossil manifest. The matcher here decides which overlay entries to
// strip before they reach libfossil.Commit.
//
// Resolution order on VFS.New:
//   1. Config.IgnoreGlob (Go-API override) wins if non-empty.
//   2. else the "vfs-ignore-glob" key from the repo's config table.
//   3. else DefaultIgnoreGlob (built-in cross-platform list).
//
// The special value "-" disables filtering entirely — commits every
// overlay row, sidecars included. Useful when sidecar round-trip
// (xattr persistence) matters more than a clean history.
//
// Glob syntax: comma-separated patterns. Each pattern is matched
// against the basename if it has no "/", otherwise against the full
// path. Standard path.Match semantics (the same flavour Fossil uses
// for its own ignore-glob setting).

package vfs

import (
	"path"
	"strings"

	"github.com/danmestas/fslite/engine"
)

// DefaultIgnoreGlob is the built-in cross-platform sentinel set. It's
// applied when neither Config.IgnoreGlob nor the repo's vfs-ignore-glob
// config key is set.
//
// Intentionally does NOT include .git/, .vscode/, .idea/, node_modules/,
// or similar — those are legitimate user-controlled directories. Users
// who want them stripped can set vfs-ignore-glob explicitly.
var DefaultIgnoreGlob = strings.Join([]string{
	// macOS — kernel WebDAV sidecars + Finder + Spotlight + Time Machine
	"._*",
	".DS_Store",
	".AppleDouble",
	".AppleDB",
	".AppleDesktop",
	".LSOverride",
	".Spotlight-V100",
	".Trashes",
	".fseventsd",
	".TemporaryItems",
	".VolumeIcon.icns",
	".com.apple.timemachine.donotpresent",
	".metadata_never_index",
	".metadata_never_index_unless_rootfs",
	".metadata_direct_scope_only",

	// Windows
	"Thumbs.db",
	"ehthumbs.db",
	"Desktop.ini",
	"$RECYCLE.BIN",

	// Linux desktops, trash, NFS lock files
	".Trash-*",
	".directory",
	".nfs*",

	// Editor swap / backup / autosave
	"*.swp",
	"*.swo",
	"*.swx",
	"*~",
	".#*",
	"#*#",
	"*.bak",
	"*.orig",
}, ",")

// FossilIgnoreKey is the repo-config table key the VFS reads + writes
// for its ignore glob. Independent of Fossil's own `ignore-glob`
// (different semantics — Fossil's is "skip on `fossil add`"; ours is
// "strip at commit drain").
const FossilIgnoreKey = "vfs-ignore-glob"

// disableIgnoreSentinel is the special value (in Config.IgnoreGlob or
// the config table) that turns off filtering completely.
const disableIgnoreSentinel = "-"

// ignoreMatcher is built from a comma-separated glob list.
type ignoreMatcher struct {
	basenamePatterns []string
	pathPatterns     []string
	disabled         bool
	source           string // "config", "default", "override", or "disabled"
	raw              string // the original glob string
}

func newIgnoreMatcher(globList, source string) *ignoreMatcher {
	m := &ignoreMatcher{source: source, raw: globList}
	if globList == disableIgnoreSentinel {
		m.disabled = true
		return m
	}
	for _, p := range strings.Split(globList, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if strings.Contains(p, "/") {
			m.pathPatterns = append(m.pathPatterns, p)
		} else {
			m.basenamePatterns = append(m.basenamePatterns, p)
		}
	}
	return m
}

// match returns true when filePath should be stripped from commits.
func (m *ignoreMatcher) match(filePath string) bool {
	if m.disabled {
		return false
	}
	base := path.Base(filePath)
	for _, p := range m.basenamePatterns {
		if ok, _ := path.Match(p, base); ok {
			return true
		}
	}
	for _, p := range m.pathPatterns {
		if ok, _ := path.Match(p, filePath); ok {
			return true
		}
	}
	return false
}

// effective returns the matcher's source + raw glob for diagnostics
// (used by `fslite ignore` to report which list is active).
func (m *ignoreMatcher) effective() (source, raw string) {
	return m.source, m.raw
}

// resolveIgnore implements the Go-API → config-table → default chain
// described at the top of this file. The engine's repo is consulted for
// the persisted setting.
func resolveIgnore(eng *engine.Engine, override string) *ignoreMatcher {
	if override != "" {
		return newIgnoreMatcher(override, "override")
	}
	if v, err := eng.Repo().Config(FossilIgnoreKey); err == nil && v != "" {
		return newIgnoreMatcher(v, "config")
	}
	return newIgnoreMatcher(DefaultIgnoreGlob, "default")
}
