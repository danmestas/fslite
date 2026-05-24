package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/danmestas/libfossil"

	"github.com/danmestas/fslite/vfs"
)

// TestMCPServerRoundtrip exercises the fslite MCP tool surface end-to-
// end using the SDK's in-process pipe transport. No subprocess, no
// stdio framing — both sides of the protocol run in the same Go test
// process, talking through a Pipe transport.
//
// The test mirrors the realistic agent workflow:
//   1. list root → see the seed
//   2. write a new file
//   3. read it back, verify bytes
//   4. commit, verify {rid, uuid} non-zero
//   5. delete a file, verify it's gone from list
//   6. ignore_set + ignore_get roundtrip
func TestMCPServerRoundtrip(t *testing.T) {
	dir := t.TempDir()
	repoPath := filepath.Join(dir, "mcp-test.fossil")

	r, err := libfossil.Create(repoPath, libfossil.CreateOpts{User: "seed"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, _, err = r.Commit(libfossil.CommitOpts{
		Files: []libfossil.FileToCommit{
			{Name: "README.md", Content: []byte("seed\n")},
		},
		Comment: "seed", User: "seed", Time: time.Now().UTC(),
	})
	if err != nil {
		r.Close()
		t.Fatalf("Commit: %v", err)
	}
	r.Close()

	v, err := vfs.New(vfs.Config{
		RepoPath:     repoPath,
		Version:      "tip",
		EnableWrites: true,
		User:         "test",
		NoNATS:       true,
	})
	if err != nil {
		t.Fatalf("vfs.New: %v", err)
	}
	defer v.Close()

	server := mcp.NewServer(&mcp.Implementation{Name: "fslite", Version: "test"}, nil)
	registerFsliteTools(server, v)

	// In-process pipe — both ends in the same test goroutine.
	cTrans, sTrans := mcp.NewInMemoryTransports()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	go func() {
		_ = server.Run(ctx, sTrans)
	}()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	session, err := client.Connect(ctx, cTrans, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer session.Close()

	call := func(toolName string, args any) (*mcp.CallToolResult, error) {
		var raw json.RawMessage
		if args != nil {
			raw, _ = json.Marshal(args)
		}
		return session.CallTool(ctx, &mcp.CallToolParams{
			Name:      toolName,
			Arguments: raw,
		})
	}

	// 1. list root
	got, err := call("list", map[string]string{"path": "."})
	if err != nil || got.IsError {
		t.Fatalf("list .: err=%v isErr=%v text=%q", err, got.IsError, firstText(got))
	}
	if !strings.Contains(firstText(got), "README.md") {
		t.Errorf("list . missing README.md: %s", firstText(got))
	}

	// 2. write
	got, err = call("write", map[string]any{
		"path":    "docs/agent.md",
		"content": "from agent via MCP",
	})
	if err != nil || got.IsError {
		t.Fatalf("write: err=%v isErr=%v text=%q", err, got.IsError, firstText(got))
	}

	// 3. read it back
	got, err = call("read", map[string]string{"path": "docs/agent.md"})
	if err != nil || got.IsError {
		t.Fatalf("read: err=%v isErr=%v text=%q", err, got.IsError, firstText(got))
	}
	if firstText(got) != "from agent via MCP" {
		t.Errorf("read returned %q", firstText(got))
	}

	// 4. commit
	got, err = call("commit", map[string]string{"message": "agent commit"})
	if err != nil || got.IsError {
		t.Fatalf("commit: err=%v isErr=%v text=%q", err, got.IsError, firstText(got))
	}
	var ck struct {
		RID  int64  `json:"rid"`
		UUID string `json:"uuid"`
	}
	json.Unmarshal([]byte(firstText(got)), &ck)
	if ck.RID == 0 || ck.UUID == "" {
		t.Errorf("commit result missing rid/uuid: %s", firstText(got))
	}

	// 5. delete + verify
	got, err = call("delete", map[string]string{"path": "README.md"})
	if err != nil || got.IsError {
		t.Fatalf("delete: err=%v isErr=%v text=%q", err, got.IsError, firstText(got))
	}
	got, err = call("list", map[string]string{"path": "."})
	if err != nil || got.IsError {
		t.Fatalf("list post-delete: %v", err)
	}
	if strings.Contains(firstText(got), "README.md") {
		t.Errorf("README.md still listed after delete: %s", firstText(got))
	}

	// 6. ignore_set + ignore_get
	got, err = call("ignore_set", map[string]string{"glob": "*.tmp,build/*"})
	if err != nil || got.IsError {
		t.Fatalf("ignore_set: %v %s", err, firstText(got))
	}
	got, err = call("ignore_get", map[string]any{})
	if err != nil || got.IsError {
		t.Fatalf("ignore_get: %v", err)
	}
	if !strings.Contains(firstText(got), "*.tmp,build/*") {
		t.Errorf("ignore_get didn't reflect set: %s", firstText(got))
	}
}

func firstText(r *mcp.CallToolResult) string {
	if r == nil || len(r.Content) == 0 {
		return ""
	}
	if t, ok := r.Content[0].(*mcp.TextContent); ok {
		return t.Text
	}
	return ""
}
