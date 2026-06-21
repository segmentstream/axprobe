#!/bin/sh
set -eu

# install.sh — download the latest axprobe release, install it, and record
# install metadata so `axprobe update` can self-replace the binary later.

REPO="segmentstream/axprobe"
BINARY="axprobe"
INSTALL_DIR="${HOME}/.axprobe/bin"
METADATA_PATH="${HOME}/.axprobe/install.json"
GITHUB_API_BASE_URL="${AXPROBE_GITHUB_API_BASE_URL:-https://api.github.com}"

usage() {
  cat <<EOF
Install axprobe.

Usage:
  install.sh [--install-dir DIR]

Options:
  --install-dir DIR  Install $BINARY into DIR. Defaults to \$HOME/.axprobe/bin.
  -h, --help         Show this help.
EOF
}

fail() {
  printf 'axprobe install: %s\n' "$1" >&2
  exit 1
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --install-dir)
      [ "$#" -ge 2 ] || fail "--install-dir requires a directory"
      INSTALL_DIR="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      fail "unknown argument: $1"
      ;;
  esac
done

command -v curl >/dev/null 2>&1 || fail "curl is required"
command -v tar >/dev/null 2>&1 || fail "tar is required"

# Detect platform; map to the goos/goarch used in release asset names.
os="$(uname -s)"
case "$os" in
  Darwin) goos="darwin" ;;
  Linux)  goos="linux" ;;
  *) fail "unsupported OS: $os" ;;
esac
arch="$(uname -m)"
case "$arch" in
  x86_64|amd64) goarch="amd64" ;;
  arm64|aarch64) goarch="arm64" ;;
  *) fail "unsupported architecture: $arch" ;;
esac

asset="${BINARY}_${goos}_${goarch}.tar.gz"

# Resolve the latest release tag + asset download URLs via the GitHub API.
api_json="$(curl -fsSL -H 'Accept: application/vnd.github+json' "${GITHUB_API_BASE_URL}/repos/${REPO}/releases/latest")" \
  || fail "could not query the latest release"

tag="$(printf '%s' "$api_json" | grep -m1 '"tag_name"' | sed 's/.*"tag_name"[^"]*"\([^"]*\)".*/\1/')"
[ -n "$tag" ] || fail "could not determine the latest release tag"
version="$(printf '%s' "$tag" | sed 's/^v//')"

base_url="https://github.com/${REPO}/releases/download/${tag}"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

printf 'Downloading %s %s\n' "$BINARY" "$tag" >&2
curl -fsSL "${base_url}/${asset}" -o "${tmp}/${asset}" || fail "could not download ${asset}"
curl -fsSL "${base_url}/checksums.txt" -o "${tmp}/checksums.txt" || fail "could not download checksums.txt"

# Verify the checksum.
want="$(grep " ${asset}\$" "${tmp}/checksums.txt" | awk '{print $1}')"
[ -n "$want" ] || fail "no checksum recorded for ${asset}"
if command -v sha256sum >/dev/null 2>&1; then
  got="$(sha256sum "${tmp}/${asset}" | awk '{print $1}')"
elif command -v shasum >/dev/null 2>&1; then
  got="$(shasum -a 256 "${tmp}/${asset}" | awk '{print $1}')"
else
  fail "need sha256sum or shasum to verify the download"
fi
[ "$want" = "$got" ] || fail "checksum mismatch for ${asset} (expected ${want}, got ${got})"

# Extract and install.
tar -xzf "${tmp}/${asset}" -C "$tmp"
[ -f "${tmp}/${BINARY}" ] || fail "${BINARY} not found in archive"
mkdir -p "$INSTALL_DIR"
chmod +x "${tmp}/${BINARY}"
mv "${tmp}/${BINARY}" "${INSTALL_DIR}/${BINARY}"

# Record install metadata so `axprobe update` can self-replace.
mkdir -p "$(dirname "$METADATA_PATH")"
cat > "$METADATA_PATH" <<EOF
{
  "method": "script",
  "install_dir": "${INSTALL_DIR}",
  "repo": "${REPO}",
  "version": "${version}",
  "os": "${goos}",
  "arch": "${goarch}"
}
EOF

# Symlink into ~/.local/bin when that exists and is writable.
LOCAL_BIN="${HOME}/.local/bin"
if [ -d "$LOCAL_BIN" ] && [ -w "$LOCAL_BIN" ]; then
  ln -sf "${INSTALL_DIR}/${BINARY}" "${LOCAL_BIN}/${BINARY}"
fi

printf 'Installed %s %s to %s\n' "$BINARY" "$version" "${INSTALL_DIR}/${BINARY}" >&2
case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *":$LOCAL_BIN:"*) ;;
  *) printf 'Add %s to your PATH to run %s.\n' "$INSTALL_DIR" "$BINARY" >&2 ;;
esac
