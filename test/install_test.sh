#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

echo "==> Running standalone install.sh integration test harness..."

TEST_DIR=$(mktemp -d)
trap 'rm -rf "$TEST_DIR"' EXIT INT TERM

# 1. Build test binary
mkdir -p "$TEST_DIR/bin"
go build -o "$TEST_DIR/bin/actr" ./cmd/actr

# 2. Package tarball
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64) ARCH="amd64" ;;
  arm64|aarch64) ARCH="arm64" ;;
  *) echo "Unsupported arch: $ARCH" && exit 1 ;;
esac

VERSION="0.99.0"
TARBALL="actr_${VERSION}_${OS}_${ARCH}.tar.gz"

mkdir -p "$TEST_DIR/fixture"
tar -czf "$TEST_DIR/fixture/$TARBALL" -C "$TEST_DIR/bin" actr

# 3. Create checksums.txt
if command -v shasum >/dev/null 2>&1; then
  HASH=$(shasum -a 256 "$TEST_DIR/fixture/$TARBALL" | awk '{print $1}')
else
  HASH=$(sha256sum "$TEST_DIR/fixture/$TARBALL" | awk '{print $1}')
fi
echo "$HASH  $TARBALL" > "$TEST_DIR/fixture/checksums.txt"

# 4. Start local HTTP server
PORT=18392
(cd "$TEST_DIR/fixture" && python3 -m http.server "$PORT" --bind 127.0.0.1 >/dev/null 2>&1) &
SERVER_PID=$!
trap 'kill "$SERVER_PID" 2>/dev/null || true; rm -rf "$TEST_DIR"' EXIT INT TERM

# Wait for server to start
for _ in {1..20}; do
  if curl -fs "http://127.0.0.1:$PORT/checksums.txt" >/dev/null 2>&1; then
    break
  fi
  sleep 0.1
done

# 5. Execute install.sh test
DEST_DIR="$TEST_DIR/dest"
FAKE_HOME="$TEST_DIR/fake_home"
mkdir -p "$DEST_DIR" "$FAKE_HOME"

ACTR_VERSION="v$VERSION" \
ACTR_DOWNLOAD_BASE_URL="http://127.0.0.1:$PORT" \
ACTR_INSTALL_DIR="$DEST_DIR" \
HOME="$FAKE_HOME" \
/bin/sh "$REPO_ROOT/install.sh"

# 6. Verify assertions
test -x "$DEST_DIR/actr"
PROBE_OUT=$("$DEST_DIR/actr" -v)
echo "Verified installed binary: $PROBE_OUT"

test -f "$FAKE_HOME/.zsh/completions/_actr"
test -f "$FAKE_HOME/.local/share/bash-completion/completions/actr"
test -f "$FAKE_HOME/.config/fish/completions/actr.fish"

echo "✓ Test Case 1: Clean install and completions passed."

# 7. Checksum failure rejection test
echo "corrupt_hash  $TARBALL" > "$TEST_DIR/fixture/checksums.txt"
BAD_DEST="$TEST_DIR/bad_dest"
mkdir -p "$BAD_DEST"

set +e
ACTR_VERSION="v$VERSION" \
ACTR_DOWNLOAD_BASE_URL="http://127.0.0.1:$PORT" \
ACTR_INSTALL_DIR="$BAD_DEST" \
HOME="$FAKE_HOME" \
/bin/sh "$REPO_ROOT/install.sh" 2>/dev/null
RESULT=$?
set -e

if [ "$RESULT" -eq 0 ]; then
  echo "Error: Installer succeeded despite corrupt checksum!" >&2
  exit 1
fi

if [ -f "$BAD_DEST/actr" ]; then
  echo "Error: Binary was installed despite checksum failure!" >&2
  exit 1
fi

echo "✓ Test Case 2: Corrupt checksum rejection passed."
echo "==> All install.sh test cases PASSED successfully."
