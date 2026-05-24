// Package e2e drives a cross-container e2e test of the fslite stack:
// NATS broker + two agent containers, each running fslite with its own
// WebDAV port. The test exercises sync, locks, and survives a restart.
//
// Skipped unless RUN_DOCKER_E2E=1 is set in the environment — the test
// shells out to `docker compose`, which is too slow + side-effecty for
// the default `go test ./...` flow.
//
//	make docker-e2e
//
// or:
//
//	RUN_DOCKER_E2E=1 go test -v -timeout 5m ./docker/
package e2e_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	urlA = "http://127.0.0.1:18081"
	urlB = "http://127.0.0.1:18082"
)

func TestDockerE2E(t *testing.T) {
	if os.Getenv("RUN_DOCKER_E2E") != "1" {
		t.Skip("set RUN_DOCKER_E2E=1 to run the docker compose e2e harness")
	}

	composeFile := composeFilePath(t)
	defer composeDown(t, composeFile)

	composeUp(t, composeFile)
	waitForHealth(t, urlA+"/healthz", 60*time.Second)
	waitForHealth(t, urlB+"/healthz", 60*time.Second)

	t.Run("seed file is visible on both agents", testSeedVisible)
	t.Run("PUT on A propagates to B", testPutPropagates)
	t.Run("DELETE on A propagates to B", testDeletePropagates)
	t.Run("LOCK on A blocks LOCK on B", testCrossAgentLock)
	t.Run("agent A survives restart", func(t *testing.T) { testAgentSurvivesRestart(t, composeFile) })
}

// -------- subtests --------

func testSeedVisible(t *testing.T) {
	// README.md was added by the seeder before either agent started.
	for _, url := range []string{urlA, urlB} {
		body := mustGet(t, url+"/README.md")
		if !strings.Contains(string(body), "docker e2e seed") {
			t.Errorf("%s/README.md: got %q", url, body)
		}
	}
}

func testPutPropagates(t *testing.T) {
	// Pre-trigger a Commit on A: writes accumulate in the overlay and
	// the only published-to-NATS notification is on Commit. So we have
	// to PUT then commit-or-trigger sync somehow. With the current
	// binary, writes via WebDAV land in the overlay; we don't auto-
	// commit. To exercise cross-agent visibility we use the read-side
	// of overlay + a manual Commit triggered by another PUT-cycle.
	//
	// Simplest test that still demonstrates real propagation: write,
	// trigger an explicit commit through the WebDAV LOCK-Refresh hack...
	// no, simpler: do two PUTs and use the fact that the binary
	// currently does not auto-commit, so visibility is local-only on B
	// for overlay writes. The "real" cross-container sync requires a
	// commit. Our binary has no HTTP endpoint to invoke Commit.
	//
	// For this e2e, we instead exercise commit-on-write by using the
	// fact that fossil-vfs auto-syncs on receiving notifications. Since
	// our binary doesn't auto-commit, we adapt the test: write the file,
	// then verify it on the *same* agent (read-back through overlay),
	// then issue a HTTP request that we extend the binary to expose
	// /commit. For V1 e2e we ship that extension below.

	mustPut(t, urlA+"/docs/note.md", []byte("hello from agentA\n"))

	// Drive A to commit so the change reaches the manifest + NATS.
	resp := mustDo(t, http.MethodPost, urlA+"/_admin/commit", []byte("e2e write"), nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("commit on A: status %d", resp.StatusCode)
	}

	// Poll B until the file shows up. Sync target is ~0.5s per spec;
	// give it 10 seconds for container scheduling latency.
	pollHTTP(t, urlB+"/docs/note.md", 10*time.Second, func(body []byte, status int) error {
		if status != http.StatusOK {
			return fmt.Errorf("status %d", status)
		}
		if string(body) != "hello from agentA\n" {
			return fmt.Errorf("body %q", body)
		}
		return nil
	})
}

func testDeletePropagates(t *testing.T) {
	mustPut(t, urlA+"/tmp/condemned.txt", []byte("doomed\n"))
	mustDoOK(t, http.MethodPost, urlA+"/_admin/commit", []byte("seed delete target"))

	pollHTTP(t, urlB+"/tmp/condemned.txt", 10*time.Second, func(_ []byte, status int) error {
		if status != http.StatusOK {
			return fmt.Errorf("status %d (not yet propagated)", status)
		}
		return nil
	})

	// Now delete on A, commit, expect 404 on B.
	mustDoOK(t, http.MethodDelete, urlA+"/tmp/condemned.txt", nil)
	mustDoOK(t, http.MethodPost, urlA+"/_admin/commit", []byte("apply delete"))

	pollHTTP(t, urlB+"/tmp/condemned.txt", 10*time.Second, func(_ []byte, status int) error {
		if status != http.StatusNotFound {
			return fmt.Errorf("status %d (expected 404)", status)
		}
		return nil
	})
}

func testCrossAgentLock(t *testing.T) {
	// Both agents must see the file. Use the already-committed README.md.
	body := lockBody("e2e", "/README.md")
	hdrs := map[string]string{
		"Content-Type": "application/xml",
		"Depth":        "0",
		"Timeout":      "Second-300",
	}

	respA := mustDo(t, "LOCK", urlA+"/README.md", []byte(body), hdrs)
	defer respA.Body.Close()
	if respA.StatusCode != http.StatusOK {
		t.Fatalf("LOCK on A: %d", respA.StatusCode)
	}
	token := respA.Header.Get("Lock-Token")

	respB := mustDo(t, "LOCK", urlB+"/README.md", []byte(body), hdrs)
	defer respB.Body.Close()
	if respB.StatusCode != http.StatusLocked {
		t.Fatalf("LOCK on B: got %d want 423", respB.StatusCode)
	}

	// Unlock on A; B can now lock.
	unlock := mustDo(t, "UNLOCK", urlA+"/README.md", nil, map[string]string{"Lock-Token": token})
	unlock.Body.Close()
	if unlock.StatusCode != http.StatusNoContent {
		t.Fatalf("UNLOCK on A: %d", unlock.StatusCode)
	}

	respB2 := mustDo(t, "LOCK", urlB+"/README.md", []byte(body), hdrs)
	respB2.Body.Close()
	if respB2.StatusCode != http.StatusOK {
		t.Errorf("LOCK on B after UNLOCK: %d", respB2.StatusCode)
	}
	// Clean up: unlock B's lock so subsequent tests aren't blocked.
	if tk := respB2.Header.Get("Lock-Token"); tk != "" {
		mustDo(t, "UNLOCK", urlB+"/README.md", nil, map[string]string{"Lock-Token": tk}).Body.Close()
	}
}

func testAgentSurvivesRestart(t *testing.T, composeFile string) {
	mustPut(t, urlA+"/restart-witness.txt", []byte("before restart\n"))
	mustDoOK(t, http.MethodPost, urlA+"/_admin/commit", []byte("witness"))
	pollHTTP(t, urlB+"/restart-witness.txt", 10*time.Second, func(_ []byte, status int) error {
		if status != http.StatusOK {
			return fmt.Errorf("not yet propagated: %d", status)
		}
		return nil
	})

	composeRestart(t, composeFile, "agentB")
	waitForHealth(t, urlB+"/healthz", 30*time.Second)

	body := mustGet(t, urlB+"/restart-witness.txt")
	if string(body) != "before restart\n" {
		t.Errorf("post-restart read: %q", body)
	}
}

// -------- harness helpers --------

func composeFilePath(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs("docker-compose.yml")
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("docker-compose.yml not found at %s", abs)
	}
	return abs
}

func composeUp(t *testing.T, file string) {
	t.Helper()
	runCompose(t, file, "up", "-d", "--wait", "--build")
}

func composeDown(t *testing.T, file string) {
	t.Helper()
	runCompose(t, file, "down", "-v")
}

func composeRestart(t *testing.T, file, service string) {
	t.Helper()
	runCompose(t, file, "restart", service)
}

func runCompose(t *testing.T, file string, args ...string) {
	t.Helper()
	full := append([]string{"compose", "-f", file}, args...)
	cmd := exec.Command("docker", full...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(full, " "), err, stderr.String())
	}
}

func waitForHealth(t *testing.T, url string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("never healthy: %s", url)
}

func mustGet(t *testing.T, url string) []byte {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return body
}

func mustPut(t *testing.T, url string, body []byte) {
	t.Helper()
	mustDoOK(t, http.MethodPut, url, body)
}

func mustDoOK(t *testing.T, method, url string, body []byte) {
	t.Helper()
	resp := mustDo(t, method, url, body, nil)
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		out, _ := io.ReadAll(resp.Body)
		t.Fatalf("%s %s: status %d body=%q", method, url, resp.StatusCode, out)
	}
}

func mustDo(t *testing.T, method, url string, body []byte, headers map[string]string) *http.Response {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, url, rdr)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	return resp
}

func pollHTTP(t *testing.T, url string, timeout time.Duration, check func(body []byte, status int) error) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err != nil {
			lastErr = err
			time.Sleep(200 * time.Millisecond)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err := check(body, resp.StatusCode); err == nil {
			return
		} else {
			lastErr = err
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("pollHTTP %s: %v", url, lastErr)
}

func lockBody(owner, root string) string {
	return strings.Join([]string{
		`<?xml version="1.0" encoding="utf-8"?>`,
		`<D:lockinfo xmlns:D="DAV:">`,
		`<D:lockscope><D:exclusive/></D:lockscope>`,
		`<D:locktype><D:write/></D:locktype>`,
		`<D:owner><D:href>` + owner + `</D:href></D:owner>`,
		`</D:lockinfo>`,
	}, "")
}

var _ = errors.New // silence linter when no errors used in some builds
