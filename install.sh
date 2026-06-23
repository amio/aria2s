#!/bin/sh
set -eu

# =============================================================================
# aria2s installer — downloads the latest release, verifies checksum, installs
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/amio/aria2s/main/install.sh | sh
#
# Options (via environment variables):
#   VERSION   Install a specific version (e.g. "v0.0.3")
#   BINDIR    Install directory (default: /usr/local/bin)
# =============================================================================

# --- configuration ---
OWNER="amio"
REPO="aria2s"
BINDIR="${BINDIR:-/usr/local/bin}"

# --- color helpers (with terminal detection) ---
if [ -t 1 ]; then
  BOLD="$(tput bold 2>/dev/null || printf '')"
  BLUE="$(tput setaf 4 2>/dev/null || printf '')"
  GREEN="$(tput setaf 2 2>/dev/null || printf '')"
  YELLOW="$(tput setaf 3 2>/dev/null || printf '')"
  RED="$(tput setaf 1 2>/dev/null || printf '')"
  RESET="$(tput sgr0 2>/dev/null || printf '')"
else
  BOLD=""; BLUE=""; GREEN=""; YELLOW=""; RED=""; RESET=""
fi

info()  { printf '%s==>%s %s%s%s\n' "${BLUE}" "${RESET}" "${BOLD}" "$*" "${RESET}"; }
ok()    { printf '%s==>%s %s%s%s\n' "${GREEN}" "${RESET}" "${BOLD}" "$*" "${RESET}"; }
warn()  { printf '%sWarning:%s %s\n' "${YELLOW}" "${RESET}" "$*" >&2; }
err()   { printf '%sError:%s %s\n' "${RED}" "${RESET}" "$*" >&2; exit 1; }

# --- prerequisite checks ---
need_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    err "need '$1' (command not found)"
  fi
}

need_cmd uname
need_cmd mktemp
need_cmd grep
need_cmd sed
need_cmd awk
need_cmd tar

# --- downloader (curl first, wget fallback) ---
downloader() {
  if command -v curl >/dev/null 2>&1; then
    curl --fail --silent --show-error --location "$@"
  elif command -v wget >/dev/null 2>&1; then
    need_cmd wget
    # emulate curl usage: downloader "$url" -o "$output"
    local outfile=""
    local url=""
    while [ $# -gt 0 ]; do
      case "$1" in
        -o) outfile="$2"; shift 2;;
        *)  url="$1"; shift;;
      esac
    done
    if [ -n "$outfile" ]; then
      wget --quiet -O "$outfile" "$url"
    else
      wget --quiet -O - "$url"
    fi
  else
    err "need curl or wget (neither found)"
  fi
}

# --- detect os / arch ---
case "$(uname -s)" in
  Darwin) OS="darwin";;
  Linux)  OS="linux";;
  *)      err "unsupported OS: $(uname -s)";;
esac

_arch_raw="$(uname -m)"
case "$_arch_raw" in
  x86_64|amd64)  ARCH="amd64";;
  aarch64|arm64) ARCH="arm64";;
  *)             err "unsupported architecture: $_arch_raw";;
esac

info "detected: ${OS}/${ARCH}"

# --- determine version ---
if [ -n "${VERSION:-}" ]; then
  TAG="$VERSION"
  info "using specified version: ${TAG}"
else
  info "fetching latest release..."
  # use a temporary file for the JSON; some systems have very short ARG_MAX
  RELEASE_JSON="$(downloader "https://api.github.com/repos/${OWNER}/${REPO}/releases/latest" -o /dev/stdout)" \
    || err "failed to fetch release info from GitHub"
  TAG="$(echo "$RELEASE_JSON" | grep '"tag_name":' | head -1 | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/')"
  [ -n "$TAG" ] || err "could not determine latest version tag"
fi

TARBALL="${REPO}_${TAG#v}_${OS}_${ARCH}.tar.gz"
BASE_URL="https://github.com/${OWNER}/${REPO}/releases/download/${TAG}"
URL="${BASE_URL}/${TARBALL}"
CHECKSUMS_URL="${BASE_URL}/checksums.txt"

info "version: ${TAG}  →  ${TARBALL}"

# --- download ---
TMPDIR="$(mktemp -d)"
trap 'rm -rf "$TMPDIR"' EXIT

info "downloading ${URL}..."
downloader "$URL" -o "${TMPDIR}/${TARBALL}" || err "failed to download ${TARBALL}"

# --- verify checksum ---
# macOS ships /sbin/sha256sum which looks like GNU sha256sum but does NOT
# accept checksum lines on stdin with -c.  Use the native shasum on macOS
# and the GNU-style sha256sum on Linux instead.
info "verifying checksum..."
downloader "$CHECKSUMS_URL" -o "${TMPDIR}/checksums.txt" 2>/dev/null \
  || warn "could not download checksums file; skipping verification"

if [ -f "${TMPDIR}/checksums.txt" ]; then
  case "$OS" in
    darwin)
      EXPECTED="$(grep " ${TARBALL}$" "${TMPDIR}/checksums.txt" | awk '{print $1}')"
      ACTUAL="$(shasum -a 256 "${TMPDIR}/${TARBALL}" | awk '{print $1}')"
      [ "$EXPECTED" = "$ACTUAL" ] || err "checksum mismatch! expected ${EXPECTED}, got ${ACTUAL}"
      ;;
    linux)
      # filter the single line we need so sha256sum -c only checks our file
      grep " ${TARBALL}$" "${TMPDIR}/checksums.txt" > "${TMPDIR}/checksums_filtered.txt"
      (cd "$TMPDIR" && sha256sum -c --quiet checksums_filtered.txt 2>/dev/null) \
        || err "checksum verification failed"
      ;;
  esac
  ok "checksum verified"
fi

# --- extract & install ---
info "extracting..."
tar -xzf "${TMPDIR}/${TARBALL}" -C "$TMPDIR"

info "installing to ${BINDIR}/aria2s..."

mkdir -p "$BINDIR" 2>/dev/null || true

if [ -w "$BINDIR" ]; then
  cp "${TMPDIR}/aria2s" "${BINDIR}/aria2s"
else
  sudo mkdir -p "$BINDIR" 2>/dev/null || true
  sudo cp "${TMPDIR}/aria2s" "${BINDIR}/aria2s"
fi
chmod +x "${BINDIR}/aria2s"

ok "aria2s ${TAG} installed to ${BINDIR}/aria2s"

# --- post-install ---
printf '\n'
if [ "$OS" = "linux" ] && ! command -v systemctl >/dev/null 2>&1; then
  warn "systemctl not found; background service setup requires systemd --user"
  info "To set up the service later:  aria2s install --start"
  info "To open the TUI console:      aria2s"
else
  info "setting up aria2c background service..."
  "${BINDIR}/aria2s" install --start
fi
