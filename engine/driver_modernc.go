//go:build !wasip1 && !js

package engine

// Native builds use the modernc.org/sqlite driver (pure-Go, fast).
// Wasm targets pick up driver_ncruces.go instead, which embeds SQLite
// as WASM and runs it via wazero — slower but the only fully-portable
// path under wasip1.
import _ "github.com/danmestas/libfossil/db/driver/modernc"
