package vfs_test

import (
	"os/exec"
	"runtime"
	"testing"
)

// TestWASMBuilds verifies that the production code cross-compiles to both
// WASM targets the spec calls out (§8 portability matrix). It does not
// run the resulting binaries — runtime exercise is gated on a WASM-friendly
// SQLite driver (modernc.org/libc currently lacks wasip1 build files;
// the ncruces driver covers GOOS=js via go-sqlite3-opfs but not wasip1).
//
// Build-time verification is the right granularity for this layer: it
// catches accidental dependencies on host-only syscalls in our own code
// and in the deps we pull (libfossil, nats.go, x/net/webdav). Runtime
// validation is the next bar once the driver story lands.
func TestWASMBuilds(t *testing.T) {
	if runtime.GOOS == "wasip1" || runtime.GOOS == "js" {
		t.Skip("already running under WASM; no cross-compile to do")
	}
	for _, target := range []struct{ os, arch string }{
		{"wasip1", "wasm"},
		{"js", "wasm"},
	} {
		t.Run(target.os+"/"+target.arch, func(t *testing.T) {
			cmd := exec.Command("go", "build", "-buildvcs=false",
				"../engine/...", "../vfs/...")
			cmd.Env = append(cmd.Environ(),
				"GOOS="+target.os,
				"GOARCH="+target.arch,
			)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("cross-compile failed (%s/%s):\n%s\n%v",
					target.os, target.arch, out, err)
			}
		})
	}
}
