#!/bin/bash
# Verifies the "mount a Fossil repo as a shared folder" use case on Linux:
# a repo built and inspected with the real fossil CLI, served by fslite,
# mounted with davfs2, edited through the mount the way an editor does, then
# checked back out with fossil.
#
# davfs2's locking is deliberately left at its default. Turning it off hides
# the LOCK + MOVE-with-If exchange that a real editor save produces, which is
# exactly where this used to break.
#
# Expects: fslite built at ./fslite, and fossil + davfs2 installed.
set -u

pass=0; fail=0
ok()   { echo "  PASS  $*"; pass=$((pass+1)); }
bad()  { echo "  FAIL  $*"; fail=$((fail+1)); }
step() { echo; echo "== $* =="; }

WORK="${RUNNER_TEMP:-/tmp}/fslite-davfs2"
MNT="${WORK}/mnt"
FSLITE="$(pwd)/fslite"
rm -rf "$WORK"; mkdir -p "$WORK"

step "environment"
uname -srm
fossil version | head -1
"$FSLITE" --help >/dev/null 2>&1 && echo "fslite binary runs"

step "build a repo with the real fossil CLI"
cd "$WORK"
fossil init biz.fossil >/dev/null
mkdir -p wc && cd wc && fossil open ../biz.fossil >/dev/null
mkdir -p contracts notes
printf 'ACME Master Services Agreement\n\nTerm: 12 months.\n' > contracts/msa.md
printf 'Q3 planning notes\n- hire 2\n' > notes/q3.md
fossil add . >/dev/null && fossil commit -m "initial business docs" >/dev/null
BASE=$(fossil timeline -n 1 -t ci -R ../biz.fossil | grep -oE '\[[0-9a-f]{10}\]' | head -1 | tr -d '[]')
echo "base check-in: $BASE"
echo "hash-policy:   $(sqlite3 "$WORK/biz.fossil" "select value from config where name='hash-policy';")"
cd "$WORK"

step "serve it with fslite"
"$FSLITE" serve --repo "$WORK/biz.fossil" --http 127.0.0.1:8080 \
    --no-nats --agent=biz > "$WORK/fslite.log" 2>&1 &
for _ in $(seq 1 100); do
  code=$(curl -s -o /dev/null -w "%{http_code}" http://127.0.0.1:8080/ 2>/dev/null)
  [ "$code" != "000" ] && break; sleep 0.1
done
[ "$code" != "000" ] && ok "daemon answering on :8080 (GET / -> $code)" || bad "daemon did not come up"

step "WebDAV protocol level (cadaver / curl)"
curl -fsS -X PROPFIND -H 'Depth: 1' http://127.0.0.1:8080/ | grep -q 'contracts' \
  && ok "PROPFIND lists the repo tree" || bad "PROPFIND did not list the tree"
echo "ls /contracts" | cadaver http://127.0.0.1:8080 2>/dev/null | grep -q 'msa.md' \
  && ok "cadaver can list a subdirectory" || bad "cadaver listing failed"

step "mount it as a filesystem (davfs2)"
sudo mkdir -p "$MNT"
printf 'ask_auth 0\n' | sudo tee -a /etc/davfs2/davfs2.conf >/dev/null
if sudo mount -t davfs -o rw,noexec,nosuid,nodev,uid=$(id -u),gid=$(id -g) http://127.0.0.1:8080 "$MNT" </dev/null 2>"$WORK/mount.err"; then
  ok "davfs2 mounted at $MNT"
  MOUNTED=1
else
  { bad "davfs2 mount failed"; echo "  ---- mount stderr ----"; sed "s/^/  /" "$WORK/mount.err"; }
  MOUNTED=0
fi

if [ "$MOUNTED" = "1" ]; then
  step "use it like a normal folder"
  ls "$MNT" | tr '\n' ' '; echo
  grep -q 'ACME' "$MNT"/contracts/msa.md \
    && ok "read a tracked file through the mount" || bad "could not read through the mount"

  # atomic save: write a sibling temp file, rename over the original
  printf 'ACME Master Services Agreement\n\nTerm: 24 months.  <-- renegotiated\n' > "$MNT"/contracts/.msa.tmp
  mv "$MNT"/contracts/.msa.tmp "$MNT"/contracts/msa.md
  sync
  grep -q 'renegotiated' "$MNT"/contracts/msa.md \
    && ok "atomic save (write temp + rename) survived" || bad "atomic save lost the edit"

  # a new file dropped in
  printf 'Invoice 1042\nAmount: 8400\n' > "$MNT"/contracts/invoice-1042.txt
  sync
  [ -f "$MNT"/contracts/invoice-1042.txt ] \
    && ok "new file created through the mount" || bad "new file did not appear"

  # let davfs2 flush its write cache before committing
  sleep 3
  sudo umount "$MNT" && ok "unmounted cleanly" || bad "unmount failed"
fi

step "commit through the daemon"
curl -fsS -X POST --data 'edits from the linux mount' http://127.0.0.1:8080/_admin/commit \
  && echo || bad "commit request failed"

step "verify with the real fossil CLI"
TIP=$(fossil timeline -n 1 -t ci -R "$WORK/biz.fossil" | grep -oE '\[[0-9a-f]{10}\]' | head -1 | tr -d '[]')
echo "tip check-in: $TIP"
[ "$TIP" != "$BASE" ] && ok "fslite produced a new check-in" || bad "no new check-in"

echo "--- fossil diff --brief $BASE -> tip ---"
fossil diff -R "$WORK/biz.fossil" --from "$BASE" --to tip --brief
CHANGED=$(fossil diff -R "$WORK/biz.fossil" --from "$BASE" --to tip --brief | grep -c 'CHANGED')
ADDED=$(fossil diff -R "$WORK/biz.fossil" --from "$BASE" --to tip --brief | grep -c 'ADDED')
[ "$CHANGED" = "1" ] && ok "exactly 1 CHANGED file (no whole-tree rewrite)" \
                     || bad "expected 1 CHANGED, got $CHANGED"
[ "$ADDED" = "1" ]   && ok "the new file was added" || bad "expected 1 ADDED, got $ADDED"

echo "--- F-cards at tip ---"
fossil artifact tip -R "$WORK/biz.fossil" | grep '^F '
HASHLEN=$(fossil artifact tip -R "$WORK/biz.fossil" | awk '/^F notes\/q3.md/{print length($3)}')
[ "$HASHLEN" = "64" ] && ok "SHA3-256 artifact ids (repo hash-policy respected)" \
                      || bad "expected 64-hex SHA3 ids, got length $HASHLEN"

fossil cat -r tip contracts/msa.md -R "$WORK/biz.fossil" | grep -q 'renegotiated' \
  && ok "committed content matches what was written through the mount" \
  || bad "committed content does not match"

echo "--- sidecar/junk files in the check-in? ---"
JUNK=$(fossil ls -r tip -R "$WORK/biz.fossil" | grep -cE '(^|/)\.|\.tmp$')
[ "$JUNK" = "0" ] && ok "no temp/hidden files reached the check-in" || bad "$JUNK junk files committed"

step "integrity"
fossil test-integrity -R "$WORK/biz.fossil" 2>&1 | tr '\r' '\n' | tail -2

"$FSLITE" stop --all >/dev/null 2>&1

echo
echo "==================== RESULT ===================="
echo "  passed: $pass    failed: $fail"
echo "==============================================="
[ "$fail" = "0" ]
