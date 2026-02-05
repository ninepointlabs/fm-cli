#!/bin/bash
set -e

VERSION="0.2.4"
ARCH="amd64"
if [ "$(uname -m)" = "aarch64" ]; then
    ARCH="arm64"
fi
PKG_NAME="fm-cli_${VERSION}_${ARCH}"

echo "Building fm-cli v${VERSION} for ${ARCH}..."

# Build binary
CGO_ENABLED=0 go build -ldflags "-s -w" -o fm-cli ./cmd/fm-cli

# Create package structure
mkdir -p "${PKG_NAME}/DEBIAN"
mkdir -p "${PKG_NAME}/usr/bin"
mkdir -p "${PKG_NAME}/usr/share/doc/fm-cli"

# Copy files
cp fm-cli "${PKG_NAME}/usr/bin/"
cp README.md "${PKG_NAME}/usr/share/doc/fm-cli/"
[ -f LICENSE ] && cp LICENSE "${PKG_NAME}/usr/share/doc/fm-cli/"

# Create control file
cat > "${PKG_NAME}/DEBIAN/control" << EOF
Package: fm-cli
Version: ${VERSION}
Section: mail
Priority: optional
Architecture: ${ARCH}
Depends: libc6
Recommends: libsecret-1-0
Maintainer: Tim Apple <tim@example.com>
Description: Terminal-based TUI for Fastmail
 FM-Cli is a minimalist, terminal-based user interface for Fastmail.
 Features include email reading/composing, calendar management,
 contacts, offline mode, and secure credential storage.
EOF

# Build package
dpkg-deb --build "${PKG_NAME}"

# Cleanup
rm -rf "${PKG_NAME}"
rm -f fm-cli

echo "Package created: ${PKG_NAME}.deb"
