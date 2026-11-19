#!/bin/sh
# actr (GitHub Actions Runner Daemon) Standalone Installer
# Usage: curl -fsSL https://raw.githubusercontent.com/adrijshikhar/actr/main/install.sh | sh

set -eu

# Formatting helpers
BOLD="$(tput bold 2>/dev/null || echo '')"
GREEN="$(tput setaf 2 2>/dev/null || echo '')"
YELLOW="$(tput setaf 3 2>/dev/null || echo '')"
BLUE="$(tput setaf 4 2>/dev/null || echo '')"
RED="$(tput setaf 1 2>/dev/null || echo '')"
RESET="$(tput sgr0 2>/dev/null || echo '')"

info() {
  printf "${BLUE}==>${RESET} ${BOLD}%s${RESET}\n" "$1"
}

success() {
  printf "${GREEN}✓${RESET} %s\n" "$1"
}

warn() {
  printf "${YELLOW}!${RESET} %s\n" "$1"
}

error() {
  printf "${RED}✗ Error:${RESET} %s\n" "$1" >&2
  exit 1
}

# 1. Detect OS
OS="$(uname -s)"
case "$OS" in
  Darwin*) OS="darwin" ;;
  Linux*)  OS="linux" ;;
  *)       error "Unsupported operating system: $OS. actr currently supports macOS (darwin) and Linux." ;;
esac

# 2. Detect Architecture
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64)   ARCH="amd64" ;;
  arm64|aarch64)  ARCH="arm64" ;;
  *)              error "Unsupported architecture: $ARCH. actr supports amd64 and arm64." ;;
esac

# 3. Resolve Target Version
REPO="${ACTR_REPO:-adrijshikhar/actr}"
if [ -n "${ACTR_VERSION:-}" ]; then
  VERSION="$ACTR_VERSION"
else
  info "Checking latest release of actr..."
  LATEST_JSON="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" 2>/dev/null || echo '')"
  if [ -n "$LATEST_JSON" ]; then
    VERSION="$(echo "$LATEST_JSON" | grep '"tag_name":' | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/' || echo '')"
  fi

  if [ -z "${VERSION:-}" ]; then
    # Fallback to redirect URL lookup if API rate-limited
    REDIRECT_URL="$(curl -fsSLI -o /dev/null -w "%{url_effective}" "https://github.com/${REPO}/releases/latest" 2>/dev/null || echo '')"
    VERSION="$(basename "$REDIRECT_URL")"
  fi

  if [ -z "${VERSION:-}" ] || [ "$VERSION" = "latest" ]; then
    VERSION="v0.2.0" # x-release-please-version
  fi
fi

# Clean version tag (strip leading v for archive filename)
CLEAN_VERSION="${VERSION#v}"
TARBALL_NAME="actr_${CLEAN_VERSION}_${OS}_${ARCH}.tar.gz"
BASE_URL="${ACTR_DOWNLOAD_BASE_URL:-https://github.com/${REPO}/releases/download/${VERSION}}"
DOWNLOAD_URL="${BASE_URL}/${TARBALL_NAME}"
CHECKSUMS_URL="${BASE_URL}/checksums.txt"

info "Installing actr ${VERSION} (${OS}/${ARCH})..."

# 4. Prepare Temporary Directory
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT INT TERM

# Helper: download file securely without leaking Authorization header across redirects
download_file() {
  url="$1"
  dest="$2"
  show_progress="${3:-false}"

  # Attempt 1: unauthenticated download (no token sent, completely safe from redirect leaks)
  if [ "$show_progress" = "true" ]; then
    if curl -fSL --progress-bar "$url" -o "$dest" 2>/dev/null; then
      return 0
    fi
  else
    if curl -fsSL "$url" -o "$dest" 2>/dev/null; then
      return 0
    fi
  fi

  # Attempt 2: authenticated download without leaking Authorization header across redirects
  if [ -n "${GITHUB_TOKEN:-}" ] || [ -n "${GH_TOKEN:-}" ]; then
    TOKEN="${GITHUB_TOKEN:-${GH_TOKEN:-}}"
    # Probe redirect without following, so Authorization header is sent only to the initial host
    REDIRECT_TARGET="$(curl -s -H "Authorization: token $TOKEN" -o /dev/null -w "%{redirect_url}" "$url" 2>/dev/null || echo '')"
    if [ -n "$REDIRECT_TARGET" ]; then
      # Fetch from redirect target without Authorization header
      if [ "$show_progress" = "true" ]; then
        if curl -fSL --progress-bar "$REDIRECT_TARGET" -o "$dest" 2>/dev/null; then
          return 0
        fi
      else
        if curl -fsSL "$REDIRECT_TARGET" -o "$dest" 2>/dev/null; then
          return 0
        fi
      fi
    else
      # No redirect returned; request directly with token
      if [ "$show_progress" = "true" ]; then
        if curl -fSL --progress-bar -H "Authorization: token $TOKEN" "$url" -o "$dest" 2>/dev/null; then
          return 0
        fi
      else
        if curl -fsSL -H "Authorization: token $TOKEN" "$url" -o "$dest" 2>/dev/null; then
          return 0
        fi
      fi
    fi
  fi

  return 1
}

# 5. Download Artifacts
info "Downloading ${TARBALL_NAME}..."
DOWNLOAD_SUCCESS=false

if download_file "$DOWNLOAD_URL" "$TMP_DIR/$TARBALL_NAME" true; then
  DOWNLOAD_SUCCESS=true
elif command -v gh >/dev/null 2>&1; then
  if gh release download "$VERSION" -R "$REPO" -p "$TARBALL_NAME" -D "$TMP_DIR" >/dev/null 2>&1; then
    DOWNLOAD_SUCCESS=true
    gh release download "$VERSION" -R "$REPO" -p "checksums.txt" -D "$TMP_DIR" >/dev/null 2>&1 || true
  fi
fi

if [ "$DOWNLOAD_SUCCESS" = "false" ]; then
  error "Failed to download $DOWNLOAD_URL. Please verify your internet connection, release visibility, or GitHub token."
fi

# 6. Verify Checksum (Fail-closed by default)
if [ "${ACTR_INSECURE_SKIP_CHECKSUM:-0}" = "1" ] || [ "${ACTR_INSECURE_SKIP_CHECKSUM:-0}" = "true" ]; then
  warn "ACTR_INSECURE_SKIP_CHECKSUM is set; skipping checksum verification."
else
  # 6a. Ensure checksums.txt is retrieved
  if [ ! -f "$TMP_DIR/checksums.txt" ]; then
    CHECKSUM_SUCCESS=false
    if download_file "$CHECKSUMS_URL" "$TMP_DIR/checksums.txt" false; then
      CHECKSUM_SUCCESS=true
    elif command -v gh >/dev/null 2>&1; then
      if gh release download "$VERSION" -R "$REPO" -p "checksums.txt" -D "$TMP_DIR" >/dev/null 2>&1; then
        CHECKSUM_SUCCESS=true
      fi
    fi

    if [ "$CHECKSUM_SUCCESS" = "false" ] || [ ! -f "$TMP_DIR/checksums.txt" ]; then
      error "Failed to download checksums.txt from $CHECKSUMS_URL. Aborting installation (set ACTR_INSECURE_SKIP_CHECKSUM=1 to bypass)."
    fi
  fi

  # 6b. Ensure hashing utility exists
  if command -v sha256sum >/dev/null 2>&1; then
    ACTUAL_SUM="$(sha256sum "$TMP_DIR/$TARBALL_NAME" | awk '{print $1}')"
  elif command -v shasum >/dev/null 2>&1; then
    ACTUAL_SUM="$(shasum -a 256 "$TMP_DIR/$TARBALL_NAME" | awk '{print $1}')"
  else
    error "Neither sha256sum nor shasum found in PATH. Cannot verify archive checksum (set ACTR_INSECURE_SKIP_CHECKSUM=1 to bypass)."
  fi

  info "Verifying SHA256 checksum..."
  EXPECTED_SUM="$(grep "$TARBALL_NAME" "$TMP_DIR/checksums.txt" | awk '{print $1}' || echo '')"
  if [ -z "$EXPECTED_SUM" ]; then
    error "Checksum entry for $TARBALL_NAME not found in checksums.txt."
  fi

  # 6c. Verify checksum match
  if [ "$EXPECTED_SUM" != "$ACTUAL_SUM" ]; then
    error "Checksum verification failed! Expected: $EXPECTED_SUM, Got: $ACTUAL_SUM"
  fi
  success "Checksum verified"
fi

# 7. Extract Binary
tar -xzf "$TMP_DIR/$TARBALL_NAME" -C "$TMP_DIR"
if [ ! -f "$TMP_DIR/actr" ]; then
  error "Failed to locate 'actr' binary in extracted archive."
fi

# 8. Determine Installation Directory
if [ -n "${ACTR_INSTALL_DIR:-}" ]; then
  DEST_DIR="$ACTR_INSTALL_DIR"
elif [ -w "/usr/local/bin" ]; then
  DEST_DIR="/usr/local/bin"
else
  DEST_DIR="${HOME}/.local/bin"
fi

mkdir -p "$DEST_DIR"
cp "$TMP_DIR/actr" "$DEST_DIR/actr"
chmod 755 "$DEST_DIR/actr"
if [ "$OS" = "darwin" ] && command -v xattr >/dev/null 2>&1; then
  xattr -dr com.apple.quarantine "$DEST_DIR/actr" 2>/dev/null || true
fi
success "Installed actr to ${DEST_DIR}/actr"

# 9. Install Shell Completions (best effort)
if command -v "$DEST_DIR/actr" >/dev/null 2>&1; then
  # zsh
  ZSH_COMP_DIR="${HOME}/.zsh/completions"
  if mkdir -p "$ZSH_COMP_DIR" 2>/dev/null; then
    "$DEST_DIR/actr" completion zsh > "$ZSH_COMP_DIR/_actr" 2>/dev/null || true
  fi

  # bash
  BASH_COMP_DIR="${HOME}/.local/share/bash-completion/completions"
  if mkdir -p "$BASH_COMP_DIR" 2>/dev/null; then
    "$DEST_DIR/actr" completion bash > "$BASH_COMP_DIR/actr" 2>/dev/null || true
  fi

  # fish
  FISH_COMP_DIR="${HOME}/.config/fish/completions"
  if mkdir -p "$FISH_COMP_DIR" 2>/dev/null; then
    "$DEST_DIR/actr" completion fish > "$FISH_COMP_DIR/actr.fish" 2>/dev/null || true
  fi
  success "Shell completions installed for zsh, bash, fish"
fi

# 10. Check PATH
case ":$PATH:" in
  *":$DEST_DIR:"*) ;;
  *)
    warn "${DEST_DIR} is not in your PATH."
    printf "  Add it to your shell configuration:\n"
    printf "    ${BOLD}export PATH=\"%s:\$PATH\"${RESET}\n\n" "$DEST_DIR"
    ;;
esac

printf "\n${GREEN}${BOLD}actr (GitHub Actions Runner Daemon) successfully installed!${RESET}\n"
printf "Run ${BOLD}actr register${RESET} to connect a runner, or ${BOLD}actr --help${RESET} for commands.\n"
