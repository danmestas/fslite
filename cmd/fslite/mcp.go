package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/danmestas/fslite/vfs"
)

// mcpCmd runs fslite as a Model Context Protocol server over stdio.
// Any MCP-speaking agent runtime gets file operations as MCP tools
// backed by the VFS — no WebDAV mount, no kernel filesystem hop,
// no /Volumes/ plumbing.
//
// Typical agent config:
//
//	{
//	  "mcpServers": {
//	    "fslite": {
//	      "command": "fslite",
//	      "args": ["mcp", "--repo", "/path/to/repo.fossil"]
//	    }
//	  }
//	}
//
// Tools exposed: list, stat, read, write, mkdir, delete, rename,
// commit, ignore_get, ignore_set.
type mcpCmd struct {
	RepoPath string `name:"repo" env:"REPO_PATH" help:"Fossil repo path (required)."`
	SeedRepo string `name:"seed" env:"SEED_REPO" help:"Optional seed repo to copy on first start."`
	User     string `name:"user" default:"fslite-mcp" help:"Fossil user attribution for commits issued via the commit tool."`
	Version  string `name:"version" default:"tip" help:"Checkin to mount initially (tip / trunk / branch / UUID)."`
	NoNATS   bool   `name:"no-nats" env:"FSLITE_NO_NATS" default:"true" help:"Local-only mode (skip NATS even if NATS_URL is set)."`
}

func (m *mcpCmd) Run() error {
	if m.RepoPath == "" {
		return fmt.Errorf("fslite mcp: --repo / REPO_PATH is required")
	}
	if err := ensureRepo(m.RepoPath, m.SeedRepo, ""); err != nil {
		return fmt.Errorf("repo bootstrap: %w", err)
	}

	v, err := vfs.New(vfs.Config{
		RepoPath:     m.RepoPath,
		Version:      m.Version,
		EnableWrites: true,
		User:         m.User,
		NoNATS:       m.NoNATS,
	})
	if err != nil {
		return fmt.Errorf("vfs.New: %w", err)
	}
	defer v.Close()

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "fslite",
		Version: "0.1.0",
	}, nil)
	registerFsliteTools(server, v)

	// stderr is safe for diagnostics — only stdout is the MCP transport.
	fmt.Fprintf(os.Stderr, "fslite mcp: serving %s over stdio\n", m.RepoPath)
	return server.Run(context.Background(), &mcp.StdioTransport{})
}

// ---------------- tool registration ----------------

func registerFsliteTools(server *mcp.Server, v *vfs.VFS) {
	// list
	type listArgs struct {
		Path string `json:"path" jsonschema:"directory path; '.' for the root"`
	}
	type listEntry struct {
		Name  string `json:"name"`
		IsDir bool   `json:"isDir"`
		Size  int64  `json:"size,omitempty"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list",
		Description: "List directory entries (one level deep). Returns JSON array of {name, isDir, size}.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args listArgs) (*mcp.CallToolResult, any, error) {
		entries, err := fs.ReadDir(v, normPath(args.Path))
		if err != nil {
			return errorResult(err), nil, nil
		}
		out := make([]listEntry, 0, len(entries))
		for _, e := range entries {
			le := listEntry{Name: e.Name(), IsDir: e.IsDir()}
			if !e.IsDir() {
				if info, err := e.Info(); err == nil {
					le.Size = info.Size()
				}
			}
			out = append(out, le)
		}
		return jsonResult(out)
	})

	// stat
	type statArgs struct {
		Path string `json:"path"`
	}
	type statResult struct {
		Name    string `json:"name"`
		Size    int64  `json:"size"`
		IsDir   bool   `json:"isDir"`
		Mode    string `json:"mode"`
		ModTime string `json:"modTime"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "stat",
		Description: "Return metadata for a single path: {name, size, isDir, mode, modTime}.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args statArgs) (*mcp.CallToolResult, any, error) {
		info, err := v.Stat(normPath(args.Path))
		if err != nil {
			return errorResult(err), nil, nil
		}
		return jsonResult(statResult{
			Name:    info.Name(),
			Size:    info.Size(),
			IsDir:   info.IsDir(),
			Mode:    info.Mode().String(),
			ModTime: info.ModTime().Format(time.RFC3339),
		})
	})

	// read
	type readArgs struct {
		Path   string `json:"path"`
		Base64 bool   `json:"base64,omitempty" jsonschema:"if true, content is base64-encoded for binary safety; default false returns plain text"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "read",
		Description: "Read a file's contents. Returns plain text by default; set base64=true for binary files.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args readArgs) (*mcp.CallToolResult, any, error) {
		f, err := v.Open(normPath(args.Path))
		if err != nil {
			return errorResult(err), nil, nil
		}
		defer f.Close()
		data, err := io.ReadAll(f)
		if err != nil {
			return errorResult(err), nil, nil
		}
		text := string(data)
		if args.Base64 {
			text = base64.StdEncoding.EncodeToString(data)
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: text}},
		}, nil, nil
	})

	// write
	type writeArgs struct {
		Path     string `json:"path"`
		Content  string `json:"content" jsonschema:"file content; if base64=true this is decoded first"`
		Base64   bool   `json:"base64,omitempty"`
		Executable bool `json:"executable,omitempty" jsonschema:"set the executable bit (mode 0755)"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "write",
		Description: "Write a file (creates parent dirs implicitly). Lands in the overlay; commit later to persist into the Fossil manifest.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args writeArgs) (*mcp.CallToolResult, any, error) {
		data := []byte(args.Content)
		if args.Base64 {
			decoded, err := base64.StdEncoding.DecodeString(args.Content)
			if err != nil {
				return errorResult(fmt.Errorf("base64 decode: %w", err)), nil, nil
			}
			data = decoded
		}
		mode := fs.FileMode(0644)
		if args.Executable {
			mode = 0755
		}
		f, err := v.OpenFile(normPath(args.Path),
			os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
		if err != nil {
			return errorResult(err), nil, nil
		}
		if w, ok := f.(io.Writer); ok {
			if _, err := w.Write(data); err != nil {
				f.Close()
				return errorResult(err), nil, nil
			}
		} else {
			f.Close()
			return errorResult(fmt.Errorf("write handle not a Writer (this is an fslite bug)")), nil, nil
		}
		if err := f.Close(); err != nil {
			return errorResult(err), nil, nil
		}
		return okResult(fmt.Sprintf("wrote %d bytes to %s", len(data), args.Path))
	})

	// mkdir — Fossil tracks no empty dirs, so this is advisory; useful
	// for agents that expect mkdir semantics before write.
	type mkdirArgs struct {
		Path string `json:"path"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "mkdir",
		Description: "Advisory mkdir (Fossil tracks no empty dirs — the directory materialises when a file is written under it).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args mkdirArgs) (*mcp.CallToolResult, any, error) {
		if err := v.Mkdir(normPath(args.Path), 0755); err != nil {
			return errorResult(err), nil, nil
		}
		return okResult("ok (advisory; write a file to materialise the dir)")
	})

	// delete
	mcp.AddTool(server, &mcp.Tool{
		Name:        "delete",
		Description: "Remove a file (writes a tombstone in the overlay).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args struct {
		Path string `json:"path"`
	}) (*mcp.CallToolResult, any, error) {
		if err := v.Remove(normPath(args.Path)); err != nil {
			return errorResult(err), nil, nil
		}
		return okResult("removed " + args.Path)
	})

	// rename / move
	mcp.AddTool(server, &mcp.Tool{
		Name:        "rename",
		Description: "Rename / move a file.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args struct {
		From string `json:"from"`
		To   string `json:"to"`
	}) (*mcp.CallToolResult, any, error) {
		if err := v.Rename(normPath(args.From), normPath(args.To)); err != nil {
			return errorResult(err), nil, nil
		}
		return okResult(fmt.Sprintf("renamed %s -> %s", args.From, args.To))
	})

	// commit
	type commitArgs struct {
		Message string `json:"message"`
	}
	type commitResult struct {
		RID  int64  `json:"rid"`
		UUID string `json:"uuid"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "commit",
		Description: "Drain the overlay (all pending writes / deletes / renames) into a new Fossil check-in. Returns {rid, uuid}.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args commitArgs) (*mcp.CallToolResult, any, error) {
		msg := args.Message
		if msg == "" {
			msg = "fslite-mcp commit"
		}
		ck, err := v.Commit(msg)
		if err != nil {
			if errors.Is(err, vfs.ErrNothingToCommit) {
				return errorResult(fmt.Errorf("nothing to commit")), nil, nil
			}
			return errorResult(err), nil, nil
		}
		return jsonResult(commitResult{RID: ck.RID, UUID: ck.UUID})
	})

	// ignore_get
	type ignoreGetResult struct {
		Source string `json:"source"`
		Glob   string `json:"glob"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "ignore_get",
		Description: "Return the current effective ignore-glob and where it came from (override / config / default).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		src, glob := v.IgnoreGlob()
		return jsonResult(ignoreGetResult{Source: src, Glob: glob})
	})

	// ignore_set
	mcp.AddTool(server, &mcp.Tool{
		Name:        "ignore_set",
		Description: "Persist a new ignore-glob in the fossil config table (or '' to reset; '-' to disable filtering).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args struct {
		Glob string `json:"glob"`
	}) (*mcp.CallToolResult, any, error) {
		if err := v.SetIgnoreGlob(args.Glob); err != nil {
			return errorResult(err), nil, nil
		}
		src, glob := v.IgnoreGlob()
		return jsonResult(ignoreGetResult{Source: src, Glob: glob})
	})
}

// ---------------- helpers ----------------

// normPath translates an empty or "/" path to "." so io/fs.ValidPath
// doesn't reject it. Strips a leading "/" so MCP clients can send
// either rooted or rootless paths.
func normPath(p string) string {
	if p == "" || p == "/" {
		return "."
	}
	if len(p) > 0 && p[0] == '/' {
		return p[1:]
	}
	return p
}

func jsonResult(v any) (*mcp.CallToolResult, any, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return errorResult(err), nil, nil
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(data)}},
	}, v, nil
}

func okResult(msg string) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
	}, nil, nil
}

func errorResult(err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
	}
}
