#!/bin/sh
# Install the secret-sync binary on macOS or Linux from a GitHub release, verified by SHA-256.
#
#   sh bootstrap.sh            # latest release
#   sh bootstrap.sh v0.1.0     # a specific release
#
# Environment:
#   SECRET_SYNC_INSTALL_DIR  install folder (default ~/.local/bin)
#   SECRET_SYNC_SOURCE_DIR   testing/mirrors: take the files from this folder instead of GitHub
#   SECRET_SYNC_TARGET       testing: force the target, e.g. linux-amd64
set -eu

REPO="jkiipper/secret-sync"
VERSION="${1:-latest}"
INSTALL_DIR="${SECRET_SYNC_INSTALL_DIR:-$HOME/.local/bin}"

if [ -n "${SECRET_SYNC_TARGET:-}" ]; then
  target="$SECRET_SYNC_TARGET"
else
  os=$(uname -s | tr '[:upper:]' '[:lower:]')
  case "$os" in darwin|linux) ;; *) echo "Unsupported OS: $os (on Windows use bootstrap.ps1)" >&2; exit 1 ;; esac
  arch=$(uname -m)
  case "$arch" in x86_64|amd64) arch=amd64 ;; arm64|aarch64) arch=arm64 ;; *) echo "Unsupported CPU: $arch" >&2; exit 1 ;; esac
  target="$os-$arch"
fi
asset="secret-sync-$target"

if [ "$VERSION" = "latest" ]; then
  base="https://github.com/$REPO/releases/latest/download"
else
  base="https://github.com/$REPO/releases/download/$VERSION"
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

fetch() {
  if [ -n "${SECRET_SYNC_SOURCE_DIR:-}" ]; then
    cp "$SECRET_SYNC_SOURCE_DIR/$1" "$tmp/$1"
  else
    curl -fsSL -o "$tmp/$1" "$base/$1"
  fi
}
fetch "$asset" || { echo "No prebuilt binary for $target. Build from source: https://github.com/$REPO" >&2; exit 1; }
fetch SHA256SUMS

expected=$(awk -v f="$asset" '{ n = $2; sub(/^\*/, "", n); if (n == f) print $1 }' "$tmp/SHA256SUMS")
[ -n "$expected" ] || { echo "SHA256SUMS has no entry for $asset" >&2; exit 1; }
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$tmp/$asset" | awk '{ print $1 }')
else
  actual=$(shasum -a 256 "$tmp/$asset" | awk '{ print $1 }')
fi
if [ "$actual" != "$expected" ]; then
  echo "Checksum mismatch for $asset; NOT installing. expected $expected, got $actual" >&2
  exit 1
fi
echo "Checksum OK ($asset)"

mkdir -p "$INSTALL_DIR"
cp "$tmp/$asset" "$INSTALL_DIR/secret-sync"
chmod 0755 "$INSTALL_DIR/secret-sync"
case "$target" in darwin-*) xattr -d com.apple.quarantine "$INSTALL_DIR/secret-sync" 2>/dev/null || true ;; esac
echo "Installed $INSTALL_DIR/secret-sync"

if [ -z "${SECRET_SYNC_TARGET:-}" ]; then
  "$INSTALL_DIR/secret-sync" version
fi
case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *) echo "Note: $INSTALL_DIR is not on your PATH. Add it, e.g.: echo 'export PATH=\"$INSTALL_DIR:\$PATH\"' >> ~/.profile" ;;
esac
