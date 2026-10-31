#!/bin/sh
# Install fslite from a GitHub release.
#
#   curl -fsSL https://raw.githubusercontent.com/danmestas/fslite/main/install.sh | sh
#
# FSLITE_VERSION      release tag to install, e.g. v0.1.7 (default: the latest)
# FSLITE_INSTALL_DIR  where the binary goes (default: $HOME/.local/bin)
#
# The archive is checked against the release's SHA256SUMS before anything is
# installed. A file curl downloads carries no quarantine flag, so macOS runs
# the binary without the xattr step a browser download needs.
set -eu

repo="danmestas/fslite"
install_dir="${FSLITE_INSTALL_DIR:-$HOME/.local/bin}"

fail() {
	echo "fslite install: $*" >&2
	exit 1
}

case "$(uname -s)" in
Darwin) os=darwin ;;
Linux) os=linux ;;
*) fail "unsupported OS $(uname -s); on Windows use install.ps1" ;;
esac

case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*) fail "unsupported CPU $(uname -m)" ;;
esac

command -v curl >/dev/null || fail "curl is required"
command -v tar >/dev/null || fail "tar is required"
if command -v sha256sum >/dev/null; then
	sha256() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null; then
	sha256() { shasum -a 256 "$1" | cut -d' ' -f1; }
else
	fail "sha256sum or shasum is required"
fi

tag="${FSLITE_VERSION:-}"
if [ -z "$tag" ]; then
	# The latest-release page redirects to the tag's page; reading the redirect
	# avoids the GitHub API and its rate limit.
	url=$(curl -fsSL -o /dev/null -w '%{url_effective}' "https://github.com/$repo/releases/latest") ||
		fail "could not reach github.com"
	tag="${url##*/}"
fi
case "$tag" in
v[0-9]*) ;;
*) fail "no release found (got '$tag')" ;;
esac

name="fslite_${tag#v}_${os}_${arch}"
base="https://github.com/$repo/releases/download/$tag"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

echo "Downloading fslite $tag for $os/$arch"
curl -fsSL -o "$tmp/$name.tar.gz" "$base/$name.tar.gz" || fail "no $name.tar.gz in release $tag"
curl -fsSL -o "$tmp/SHA256SUMS" "$base/SHA256SUMS" || fail "no SHA256SUMS in release $tag"

want=$(awk -v f="$name.tar.gz" '$2 == f { print $1 }' "$tmp/SHA256SUMS")
[ -n "$want" ] || fail "SHA256SUMS does not list $name.tar.gz"
got=$(sha256 "$tmp/$name.tar.gz")
[ "$got" = "$want" ] || fail "checksum mismatch for $name.tar.gz: got $got, want $want"

tar -xzf "$tmp/$name.tar.gz" -C "$tmp"
[ -f "$tmp/$name/fslite" ] || fail "$name.tar.gz has no fslite binary"
mkdir -p "$install_dir"
# Install through a temporary name so a running fslite is replaced, not
# overwritten in place.
cp "$tmp/$name/fslite" "$install_dir/.fslite.new"
chmod 755 "$install_dir/.fslite.new"
mv -f "$install_dir/.fslite.new" "$install_dir/fslite"

echo "Installed $install_dir/fslite ($tag)"
case ":$PATH:" in
*":$install_dir:"*) ;;
*) echo "Add $install_dir to your PATH to run fslite from anywhere." ;;
esac
