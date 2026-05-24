//go:build wasip1 || js

package engine

// WASM targets use the ncruces driver: a pure-Go SQLite driver that
// embeds CGo-free SQLite via wazero. Works under wasip1 (host syscalls
// for filesystem) and under js (where go-sqlite3-opfs is layered on top
// for OPFS storage; the caller wires that in).
import _ "github.com/danmestas/libfossil/db/driver/ncruces"
