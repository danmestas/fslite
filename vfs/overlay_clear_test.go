package vfs

import "testing"

// Commit drains a snapshot of the overlay and then clears it. A write that
// lands after the snapshot was not committed, so the clear must leave it,
// in memory and in the table a restart reloads, while the drained entries,
// an empty file among them, go.
func TestOverlayClearSnapshotKeepsLaterWrites(t *testing.T) {
	v := newSeededVFS(t, "seed.txt", "seed\n")
	writeContent(t, v, "drained.txt", "committed\n")
	writeContent(t, v, "empty.txt", "")
	// A non-nil empty slice is stored as a zero-length blob, not NULL, while
	// the map holds it as nil.
	if err := v.overlayPutContent("blob.txt", []byte{}, "", 1); err != nil {
		t.Fatalf("overlayPutContent: %v", err)
	}
	writeContent(t, v, "raced.txt", "first\n")
	snap := v.overlayList()

	writeContent(t, v, "raced.txt", "second, after the snapshot\n")
	writeContent(t, v, "late.txt", "new after the snapshot\n")
	if err := v.overlayClearSnapshot(snap); err != nil {
		t.Fatalf("overlayClearSnapshot: %v", err)
	}

	want := map[string]string{
		"raced.txt": "second, after the snapshot\n",
		"late.txt":  "new after the snapshot\n",
	}
	for _, gone := range []string{"drained.txt", "empty.txt", "blob.txt"} {
		if _, ok := v.overlayLookup(gone); ok {
			t.Errorf("%s still in the overlay map after its snapshot was cleared", gone)
		}
	}
	for name, content := range want {
		e, ok := v.overlayLookup(name)
		if !ok || string(e.content) != content {
			t.Errorf("overlay map %s = %v, want %q kept", name, e, content)
		}
	}

	rows, err := v.eng.Repo().DB().Query("SELECT path, content FROM workspace_overlay")
	if err != nil {
		t.Fatalf("query overlay: %v", err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var p string
		var c []byte
		if err := rows.Scan(&p, &c); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[p] = string(c)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if len(got) != len(want) {
		t.Errorf("overlay rows = %v, want exactly %v", got, want)
	}
	for name, content := range want {
		if got[name] != content {
			t.Errorf("overlay row %s = %q, want %q", name, got[name], content)
		}
	}
}
