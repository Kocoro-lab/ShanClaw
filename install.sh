#!/bin/sh
set -e

REPO="Kocoro-lab/ShanClaw"
INSTALL_DIR="/usr/local/bin"

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)
case "$ARCH" in
    x86_64) ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
esac

echo "Detecting platform: ${OS}/${ARCH}"

# Get latest release tag
LATEST=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | sed -E 's/.*"v?([^"]+)".*/\1/')

if [ -z "$LATEST" ]; then
    echo "Error: Could not detect latest version"
    exit 1
fi

echo "Installing shan v${LATEST}..."

FILENAME="shan_${LATEST}_${OS}_${ARCH}.tar.gz"
URL="https://github.com/${REPO}/releases/download/v${LATEST}/${FILENAME}"

TMP=$(mktemp -d)
curl -fsSL "$URL" -o "${TMP}/${FILENAME}"

# Verify checksum if checksums.txt is available (graceful degradation for older releases)
CHECKSUM_URL="https://github.com/${REPO}/releases/download/v${LATEST}/checksums.txt"
if curl -fsSL "$CHECKSUM_URL" -o "${TMP}/checksums.txt" 2>/dev/null; then
    echo "Verifying checksum..."
    (cd "$TMP" && grep "${FILENAME}" checksums.txt | shasum -a 256 -c -)
    if [ $? -ne 0 ]; then
        echo "Error: Checksum verification failed!"
        rm -rf "$TMP"
        exit 1
    fi
else
    echo "Warning: checksums.txt not available, skipping verification"
fi

tar -xzf "${TMP}/${FILENAME}" -C "$TMP"

if [ -w "$INSTALL_DIR" ]; then
    mv "${TMP}/shan" "${INSTALL_DIR}/shan"
else
    sudo mv "${TMP}/shan" "${INSTALL_DIR}/shan"
fi

rm -rf "$TMP"

echo "shan v${LATEST} installed to ${INSTALL_DIR}/shan"
echo "Run 'shan' to get started."
