package vfs

import "testing"

func TestIgnoreMatcher(t *testing.T) {
	m := newIgnoreMatcher(DefaultIgnoreGlob, "default")

	shouldIgnore := []string{
		// macOS
		".DS_Store",
		"src/.DS_Store",
		"._foo.txt",
		"deep/nested/._x",
		".Spotlight-V100",
		".fseventsd",
		// Windows
		"Thumbs.db",
		"Desktop.ini",
		// Linux desktops
		".Trash-1000",
		// editor swap / backup
		"main.swp",
		"src/main.swp",
		"buffer.swo",
		"buffer.swx",
		"draft.txt~",
		".#emacs-lock",
		"#emacs-auto#",
		"old.bak",
		"merged.orig",
	}
	for _, p := range shouldIgnore {
		if !m.match(p) {
			t.Errorf("default matcher: %q should be ignored", p)
		}
	}

	shouldKeep := []string{
		// legit dotfiles
		".gitignore",
		".env",
		".bashrc",
		".vimrc",
		".profile",
		// regular files
		"README.md",
		"src/main.go",
		"file.txt",
		// look-alikes that aren't exact matches
		"DS_Store.txt",
		"my.swap",
	}
	for _, p := range shouldKeep {
		if m.match(p) {
			t.Errorf("default matcher: %q should be kept", p)
		}
	}
}

func TestIgnoreMatcherCustomGlob(t *testing.T) {
	// Custom: strip only build artefacts; let macOS sidecars through.
	m := newIgnoreMatcher("*.o,*.tmp,build/*", "config")

	cases := map[string]bool{
		"foo.o":           true,
		"src/main.o":      true,
		"draft.tmp":       true,
		"build/x":         true,
		// ".DS_Store" not in this list → keep
		".DS_Store":       false,
		"._foo.txt":       false,
		"src/main.go":     false,
	}
	for p, want := range cases {
		got := m.match(p)
		if got != want {
			t.Errorf("custom matcher %q: got %v want %v", p, got, want)
		}
	}
}

func TestIgnoreMatcherDisabled(t *testing.T) {
	m := newIgnoreMatcher(disableIgnoreSentinel, "disabled")
	for _, p := range []string{".DS_Store", "._foo", "anything"} {
		if m.match(p) {
			t.Errorf("disabled matcher should never match; got match on %q", p)
		}
	}
}

func TestIgnoreMatcherEmpty(t *testing.T) {
	// Empty glob string parses to a no-op matcher (no patterns) — nothing
	// matches but it's not "disabled".
	m := newIgnoreMatcher("", "empty")
	if m.disabled {
		t.Error("empty glob shouldn't set disabled flag")
	}
	if m.match(".DS_Store") {
		t.Error("empty glob shouldn't match anything")
	}
}

func TestIgnoreMatcherFullPathPattern(t *testing.T) {
	m := newIgnoreMatcher("vendor/*,*.lock", "config")
	cases := map[string]bool{
		"vendor/x":          true,
		"go.lock":           true,
		"src/main.go":       false,
		"vendor/deep/x.go":  false, // path.Match doesn't recurse without **
	}
	for p, want := range cases {
		got := m.match(p)
		if got != want {
			t.Errorf("%q: got %v want %v", p, got, want)
		}
	}
}
