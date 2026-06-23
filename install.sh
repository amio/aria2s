#!/bin/sh
set -eu

# --- configuration ---
OWNER="amio"
REPO="aria2s"
BINDIR="${BINDIR:-/usr/local/bin}"

# --- helpers ---
info()  { printf "\033[1;34m==>\033[0m \033[1m%s\033[0m\n" "$*"; }
ok()    { printf "\033[1;32m==>\033[0m \033[1m%s\033[0m\n" "$*"; }
warn()  { printf "\033[1;33mWarning:\033[0m %s\n" "$*" >&2; }
err()   { printf "\033[1;31mError:\033[0m %s\n" "$*" >&2; exit 1; }

# detect os / arch
case "$(uname -s)" in
  Darwin) OS="darwin";;
  Linux)  OS="linux";;
  *)      err "unsupported OS: $(uname -s)";;
esac

case "$(uname -m)" in
  x86_64|amd64)  ARCH="amd64";;
  aarch64|arm64) ARCH="arm64";;
  *)             err "unsupported architecture: $(uname -m)";;
esac

info "detected: ${OS}/${ARCH}"

# --- fetch latest release ---
info "fetching latest release..."
RELEASE_JSON=$(curl -fsSL "https://api.github.com/repos/${OWNER}/${REPO}/releases/latest") || err "failed to fetch release info"
TAG=$(echo "$RELEASE_JSON" | grep '"tag_name":' | head -1 | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/')
[ -n "$TAG" ] || err "could not determine latest tag"

TARBALL="${REPO}_${TAG#v}_${OS}_${ARCH}.tar.gz"
URL="https://github.com/${OWNER}/${REPO}/releases/download/${TAG}/${TARBALL}"
CHECKSUMS_URL="https://github.com/${OWNER}/${REPO}/releases/download/${TAG}/checksums.txt"

info "latest: ${TAG}  →  ${TARBALL}"

# --- download ---
TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT

info "downloading ${URL}..."
curl -fsSLo "${TMPDIR}/${TARBALL}" "$URL" || err "download failed"

# verify checksum if sha256sum is available
if command -v sha256sum >/dev/null 2>&1; then
  info "verifying checksum..."
  curl -fsSLo "${TMPDIR}/checksums.txt" "$CHECKSUMS_URL" 2>/dev/null || warn "could not download checksums, skipping verification"
  if [ -f "${TMPDIR}/checksums.txt" ]; then
    (cd "$TMPDIR" && grep " ${TARBALL}$" checksums.txt | sha256sum -c --quiet 2>/dev/null) || err "checksum mismatch! aborting"
    ok "checksum verified"
  fi
elif command -v shasum >/dev/null 2>&1; then
  info "verifying checksum..."
  curl -fsSLo "${TMPDIR}/checksums.txt" "$CHECKSUMS_URL" 2>/dev/null || warn "could not download checksums, skipping verification"
  if [ -f "${TMPDIR}/checksums.txt" ]; then
    EXPECTED=$(grep " ${TARBALL}$" "${TMPDIR}/checksums.txt" | awk '{print $1}')
    ACTUAL=$(shasum -a 256 "${TMPDIR}/${TARBALL}" | awk '{print $1}')
    [ "$EXPECTED" = "$ACTUAL" ] || err "checksum mismatch! aborting"
    ok "checksum verified"
  fi
fi

# --- extract & install ---
info "extracting..."
tar -xzf "${TMPDIR}/${TARBALL}" -C "$TMPDIR"

info "installing to ${BINDIR}/aria2s..."

if [ -w "$BINDIR" ]; then
  cp "${TMPDIR}/aria2s" "${BINDIR}/aria2s"
else
  sudo cp "${TMPDIR}/aria2s" "${BINDIR}/aria2s"
fi

chmod +x "${BINDIR}/aria2s"

ok "aria2s ${TAG} installed to ${BINDIR}/aria2s"

# --- post-install ---
printf "\n"
if [ "$OS" = "linux" ] && ! command -v systemctl >/dev/null 2>&1; then
  warn "systemctl not found; background service setup requires systemd --user"
  info "To set up the service later:  aria2s install --start"
  info "To open the console later:    aria2s"
else
  info "setting up aria2c background service..."
  "${BINDIR}/aria2s" install --start
fi
