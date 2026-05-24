package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math/big"
	"path/filepath"
	"strings"
)

// nameAdjectives + nameAnimals seed the generated daemon names. About
// 60 × 60 = 3600 combos; we append a 4-hex-char nonce to collision-proof
// against the rare clash within a single agents/ directory.
var nameAdjectives = []string{
	"alpine", "amber", "ancient", "arctic", "azure", "blazing", "bold",
	"brave", "brisk", "bronze", "calm", "celestial", "clever", "cobalt",
	"crimson", "crisp", "curious", "dapper", "dewy", "eager", "earnest",
	"electric", "elegant", "fast", "fierce", "fiery", "frosty", "gallant",
	"gentle", "glacial", "glowing", "golden", "humble", "iridescent",
	"jolly", "keen", "lithe", "lucent", "lunar", "mellow", "midnight",
	"misty", "noble", "nimble", "obsidian", "patient", "placid", "plucky",
	"quick", "quiet", "radiant", "regal", "scarlet", "silent", "silver",
	"solar", "stoic", "stout", "swift", "tame", "tidy", "twilight", "valiant",
	"verdant", "vibrant", "vivid", "warm", "whimsical", "wild", "wise",
}

var nameAnimals = []string{
	"badger", "beaver", "bison", "cardinal", "cheetah", "chinchilla",
	"coyote", "crane", "deer", "dolphin", "eagle", "ermine", "falcon",
	"ferret", "finch", "fisher", "fox", "gazelle", "goose", "hare",
	"hawk", "hedgehog", "heron", "hyena", "ibex", "iguana", "jaguar",
	"jay", "kestrel", "lemur", "leopard", "lion", "lynx", "magpie",
	"marmot", "marten", "meerkat", "mink", "moose", "narwhal", "ocelot",
	"orca", "otter", "owl", "panda", "panther", "pelican", "puma",
	"quail", "raccoon", "raven", "robin", "salmon", "seal", "sloth",
	"sparrow", "stoat", "swan", "tiger", "toucan", "turtle", "vulture",
	"weasel", "wolf", "wolverine", "wren", "yak", "zebra",
}

// generateAgentName returns "adjective-animal-nonce" like "electric-hyena-a3f".
// Crypto-random so concurrent serves don't collide on the same name.
func generateAgentName() string {
	adj := nameAdjectives[randIndex(len(nameAdjectives))]
	animal := nameAnimals[randIndex(len(nameAnimals))]
	var nonce [2]byte
	_, _ = rand.Read(nonce[:])
	return fmt.Sprintf("%s-%s-%s", adj, animal, hex.EncodeToString(nonce[:]))
}

func randIndex(n int) int {
	bigN := big.NewInt(int64(n))
	v, err := rand.Int(rand.Reader, bigN)
	if err != nil {
		return 0
	}
	return int(v.Int64())
}

// resolveAgentName picks the agent name for a new daemon.
//
//   - explicit non-empty wins (the user knows what they want; we still
//     reject if a *different live* daemon already owns that name).
//   - randomFlag → generated "electric-hyena-a3f" style name.
//   - else: derived from the repo path (basename minus .fossil), with
//     "-2", "-3", … appended to avoid collision with other live daemons.
func resolveAgentName(explicit string, randomFlag bool, repoPath string) (string, error) {
	if explicit != "" {
		if existing, _ := loadAgentState(explicit); existing != nil {
			if processAlive(existing.PID) && existing.RepoPath != repoPath {
				return "", fmt.Errorf(
					"agent %q is already running with a different repo (%s); pick another --agent or stop the existing one",
					explicit, existing.RepoPath)
			}
		}
		return explicit, nil
	}
	if randomFlag {
		return generateAgentName(), nil
	}
	return deriveAgentNameFromRepo(repoPath), nil
}

// deriveAgentNameFromRepo turns "/path/to/myproject.fossil" into
// "myproject" (deconflicted). If the basename is too generic
// ("repo", "fossil", etc.), falls back to the parent dir name. If even
// that's generic, falls back to a random name.
func deriveAgentNameFromRepo(repoPath string) string {
	base := filenameStem(filepath.Base(repoPath))
	if isGenericName(base) {
		parent := filenameStem(filepath.Base(filepath.Dir(repoPath)))
		if !isGenericName(parent) && parent != "" {
			base = parent
		}
	}
	base = sanitizeName(base)
	if base == "" {
		return generateAgentName()
	}
	return deconflictName(base)
}

// filenameStem drops a .fossil / .sqlite extension if present.
func filenameStem(name string) string {
	for _, ext := range []string{".fossil", ".fossil-tmp", ".sqlite", ".sqlite3", ".db"} {
		if strings.HasSuffix(name, ext) {
			return strings.TrimSuffix(name, ext)
		}
	}
	return name
}

// sanitizeName keeps only ASCII letters / digits / hyphen / underscore.
// Any other rune is dropped. Lowercased.
func sanitizeName(name string) string {
	name = strings.ToLower(name)
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		}
	}
	return b.String()
}

// isGenericName flags names that are unhelpful as agent identifiers
// because they don't describe the project (every project has them).
func isGenericName(name string) bool {
	switch strings.ToLower(name) {
	case "", "repo", "fossil", "default", "data", "tmp",
		"temp", "test", "home", "root", "src", "main", "demo":
		return true
	}
	return false
}

// deconflictName appends -2, -3, … to base until no live daemon owns
// that name. Stale recorded names (process gone) are reclaimable.
func deconflictName(base string) string {
	for n := 1; n < 10000; n++ {
		candidate := base
		if n > 1 {
			candidate = fmt.Sprintf("%s-%d", base, n)
		}
		existing, _ := loadAgentState(candidate)
		if existing == nil {
			return candidate
		}
		if !processAlive(existing.PID) {
			return candidate
		}
	}
	return generateAgentName()
}
