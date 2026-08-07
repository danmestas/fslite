// Command fslite-seed creates an initial Fossil repository for the
// docker-compose e2e harness. Both agent containers start from a copy
// of this file so they share both the project code and an initial
// manifest (the prerequisite for libfossil sync to converge).
package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/danmestas/go-libfossil"

	// Driver registration via the build-tagged file in engine/. Importing
	// it here keeps this binary self-contained without duplicating the
	// driver-import dance.
	_ "github.com/danmestas/fslite/engine"
)

func main() {
	repoPath := envDefault("REPO_PATH", "/seed/repo.fossil")
	projectCode := envDefault("PROJECT_CODE", "")

	if projectCode == "" {
		fmt.Fprintln(os.Stderr, "fslite-seed: PROJECT_CODE is required (40-char lowercase hex)")
		os.Exit(2)
	}

	if _, err := os.Stat(repoPath); err == nil {
		log.Printf("fslite-seed: %s already exists; nothing to do", repoPath)
		return
	}
	if err := os.MkdirAll(filepath.Dir(repoPath), 0o755); err != nil {
		log.Fatalf("mkdir: %v", err)
	}

	r, err := libfossil.Create(repoPath, libfossil.CreateOpts{
		User:        "seed",
		ProjectCode: projectCode,
	})
	if err != nil {
		log.Fatalf("libfossil.Create: %v", err)
	}

	_, _, err = r.Commit(libfossil.CommitOpts{
		Files: []libfossil.FileToCommit{
			{Name: "README.md", Content: []byte("docker e2e seed\n")},
			{Name: "src/main.go", Content: []byte("package main\n")},
		},
		Comment: "seed",
		User:    "seed",
		Time:    time.Now().UTC(),
	})
	if err != nil {
		r.Close()
		log.Fatalf("seed Commit: %v", err)
	}
	if err := r.Close(); err != nil {
		log.Fatalf("Close: %v", err)
	}

	log.Printf("fslite-seed: created %s with project_code=%s", repoPath, projectCode)
}

func envDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
